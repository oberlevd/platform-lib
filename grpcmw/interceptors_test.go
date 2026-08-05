package grpcmw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/oberlevd/platform-lib/logger"
	platformmetrics "github.com/oberlevd/platform-lib/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const testMethod = "/test.TestService/Ping"

func testLogger(buf io.Writer) *logger.Logger {
	return logger.New(logger.Config{
		Service: "grpcmw-test",
		Version: "test",
		Env:     "test",
		Output:  buf,
	})
}

func testRED(t *testing.T) *platformmetrics.RED {
	t.Helper()
	reg := prometheus.NewRegistry()
	return platformmetrics.NewRED(reg, "grpcmw_test")
}

func unaryInfo() *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: testMethod}
}

func runInterceptor(
	interceptor grpc.UnaryServerInterceptor,
	ctx context.Context,
	handler grpc.UnaryHandler,
) (any, error) {
	return interceptor(ctx, struct{}{}, unaryInfo(), handler)
}

// chainInterceptors имитирует grpc.ChainUnaryInterceptor: первый в списке
// - самый внешний (вызывается первым на входе).
func chainInterceptors(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		build := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := build
			build = func(ctx context.Context, req any) (any, error) {
				return ic(ctx, req, info, next)
			}
		}
		return build(ctx, req)
	}
}

func TestRequestIDFromMetadata(t *testing.T) {
	var captured string
	handler := func(ctx context.Context, req any) (any, error) {
		captured = logger.RequestIDFromContext(ctx)
		return "ok", nil
	}
	md := metadata.Pairs("x-request-id", "incoming-req-id-001")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := runInterceptor(RequestIDUnaryInterceptor(), ctx, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if captured != "incoming-req-id-001" {
		t.Fatalf("request_id = %q, want incoming-req-id-001", captured)
	}
}

func TestRequestIDGeneratedWhenMissing(t *testing.T) {
	var captured string
	handler := func(ctx context.Context, req any) (any, error) {
		captured = logger.RequestIDFromContext(ctx)
		return "ok", nil
	}
	_, err := runInterceptor(RequestIDUnaryInterceptor(), context.Background(), handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if len(captured) != 32 {
		t.Fatalf("expected generated 32-char request_id, got %q (len=%d)", captured, len(captured))
	}
}

func TestLoggingUnaryInterceptorSuccess(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
	)
	_, err := runInterceptor(interceptor, context.Background(), handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc request completed") {
		t.Fatalf("expected success log, got: %s", raw)
	}
	if !strings.Contains(raw, testMethod) {
		t.Fatalf("expected grpc_method in log, got: %s", raw)
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %v\nraw: %s", err, raw)
	}
	if entry["grpc_code"] != "OK" {
		t.Fatalf("grpc_code = %v, want OK", entry["grpc_code"])
	}
}

func TestLoggingUnaryInterceptorError(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
	)
	_, err := runInterceptor(interceptor, context.Background(), handler)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc request failed") {
		t.Fatalf("expected error log, got: %s", raw)
	}
	if !strings.Contains(raw, "NotFound") {
		t.Fatalf("expected NotFound in log, got: %s", raw)
	}
}

func TestMetricsUnaryInterceptorCountsOK(t *testing.T) {
	m := testRED(t)
	handler := func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	}
	_, err := runInterceptor(MetricsUnaryInterceptor(m), context.Background(), handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "OK"))
	if got != 1 {
		t.Fatalf("RequestsTotal = %v, want 1", got)
	}
	inFlight := testutil.ToFloat64(m.RequestsInFlight.WithLabelValues(testMethod))
	if inFlight != 0 {
		t.Fatalf("RequestsInFlight after request = %v, want 0", inFlight)
	}
}

func TestMetricsUnaryInterceptorCountsError(t *testing.T) {
	m := testRED(t)
	handler := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.Internal, "boom")
	}
	_, _ = runInterceptor(MetricsUnaryInterceptor(m), context.Background(), handler)
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "Internal"))
	if got != 1 {
		t.Fatalf("RequestsTotal Internal = %v, want 1", got)
	}
}

func TestRecoveryUnaryInterceptorCatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(ctx context.Context, req any) (any, error) {
		panic("test-panic")
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
		RecoveryUnaryInterceptor(),
	)
	_, err := runInterceptor(interceptor, context.Background(), handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal, err=%v", status.Code(err), err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc handler panicked") {
		t.Fatalf("expected panic log, got: %s", raw)
	}
	if !strings.Contains(raw, "test-panic") {
		t.Fatalf("expected panic value in log, got: %s", raw)
	}
}

func TestChainMetricsCountPanic(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	m := testRED(t)
	handler := func(ctx context.Context, req any) (any, error) {
		panic("chain-panic")
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
		MetricsUnaryInterceptor(m),
		RecoveryUnaryInterceptor(),
	)
	_, err := runInterceptor(interceptor, context.Background(), handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
	got := testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "Internal"))
	if got != 1 {
		t.Fatalf("RequestsTotal after panic = %v, want 1", got)
	}
	inFlight := testutil.ToFloat64(m.RequestsInFlight.WithLabelValues(testMethod))
	if inFlight != 0 {
		t.Fatalf("RequestsInFlight after panic = %v, want 0", inFlight)
	}
}

func TestChainSuccess(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	m := testRED(t)
	var gotID string
	handler := func(ctx context.Context, req any) (any, error) {
		gotID = logger.RequestIDFromContext(ctx)
		l := logger.FromContext(ctx)
		l.Info(ctx, "inside handler")
		return "ok", nil
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
		MetricsUnaryInterceptor(m),
		RecoveryUnaryInterceptor(),
	)
	md := metadata.Pairs("x-request-id", "chain-req-42")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := runInterceptor(interceptor, ctx, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if gotID != "chain-req-42" {
		t.Fatalf("request_id = %q, want chain-req-42", gotID)
	}
	if testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "OK")) != 1 {
		t.Fatal("expected RequestsTotal OK = 1")
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc request completed") {
		t.Fatalf("expected completed log, got: %s", raw)
	}
	if !strings.Contains(raw, "inside handler") {
		t.Fatalf("expected handler log via FromContext, got: %s", raw)
	}
}

func TestLoggingPutsLoggerInContext(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(ctx context.Context, req any) (any, error) {
		l := logger.FromContext(ctx)
		if l == nil {
			t.Error("FromContext returned nil")
		}
		l.Info(ctx, "handler-log-marker")
		return "ok", nil
	}
	interceptor := chainInterceptors(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
	)
	_, err := runInterceptor(interceptor, context.Background(), handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !strings.Contains(buf.String(), "handler-log-marker") {
		t.Fatalf("expected handler log in output: %s", buf.String())
	}
}

func TestChainOptionMatchesOrder(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	m := testRED(t)
	opt := Chain(base, m)
	srv := grpc.NewServer(opt)
	if srv == nil {
		t.Fatal("expected non-nil server from Chain")
	}
	srv.Stop()
}
