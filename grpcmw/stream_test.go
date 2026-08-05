package grpcmw

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/oberlevd/platform-lib/logger"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context {
	return f.ctx
}

func streamInfo() *grpc.StreamServerInfo {
	return &grpc.StreamServerInfo{FullMethod: testMethod}
}

func runStreamInterceptor(
	interceptor grpc.StreamServerInterceptor,
	ss grpc.ServerStream,
	handler grpc.StreamHandler,
) error {
	return interceptor(nil, ss, streamInfo(), handler)
}

func chainStreamInterceptors(interceptors ...grpc.StreamServerInterceptor) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		build := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := build
			build = func(srv any, ss grpc.ServerStream) error {
				return ic(srv, ss, info, next)
			}
		}
		return build(srv, ss)
	}
}

func TestRequestIDStreamFromMetadata(t *testing.T) {
	var captured string
	handler := func(srv any, ss grpc.ServerStream) error {
		captured = logger.RequestIDFromContext(ss.Context())
		return nil
	}
	md := metadata.Pairs("x-request-id", "stream-req-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	ss := &fakeServerStream{ctx: ctx}
	if err := runStreamInterceptor(RequestIDStreamInterceptor(), ss, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if captured != "stream-req-1" {
		t.Fatalf("request_id = %q, want stream-req-1", captured)
	}
}

func TestRequestIDStreamGeneratedWhenMissing(t *testing.T) {
	var captured string
	handler := func(srv any, ss grpc.ServerStream) error {
		captured = logger.RequestIDFromContext(ss.Context())
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	if err := runStreamInterceptor(RequestIDStreamInterceptor(), ss, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if len(captured) != 32 {
		t.Fatalf("expected 32-char request_id, got %q", captured)
	}
}

func TestLoggingStreamSuccess(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(srv any, ss grpc.ServerStream) error {
		return nil
	}
	interceptor := chainStreamInterceptors(
		RequestIDStreamInterceptor(),
		LoggingStreamInterceptor(base),
	)
	ss := &fakeServerStream{ctx: context.Background()}
	if err := runStreamInterceptor(interceptor, ss, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc stream completed") {
		t.Fatalf("expected stream completed log, got: %s", raw)
	}
	if !strings.Contains(raw, testMethod) {
		t.Fatalf("expected method in log, got: %s", raw)
	}
}

func TestLoggingStreamError(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(srv any, ss grpc.ServerStream) error {
		return status.Error(codes.NotFound, "missing")
	}
	interceptor := chainStreamInterceptors(
		RequestIDStreamInterceptor(),
		LoggingStreamInterceptor(base),
	)
	ss := &fakeServerStream{ctx: context.Background()}
	err := runStreamInterceptor(interceptor, ss, handler)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
	if !strings.Contains(buf.String(), "grpc stream failed") {
		t.Fatalf("expected stream failed log, got: %s", buf.String())
	}
}

func TestMetricsStreamCountsOK(t *testing.T) {
	m := testRED(t)
	handler := func(srv any, ss grpc.ServerStream) error {
		return nil
	}
	ss := &fakeServerStream{ctx: context.Background()}
	if err := runStreamInterceptor(MetricsStreamInterceptor(m), ss, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "OK")) != 1 {
		t.Fatal("RequestsTotal OK != 1")
	}
	if testutil.ToFloat64(m.RequestsInFlight.WithLabelValues(testMethod)) != 0 {
		t.Fatal("RequestsInFlight != 0")
	}
}

func TestRecoveryStreamCatchesPanic(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	handler := func(srv any, ss grpc.ServerStream) error {
		panic("stream-panic")
	}
	interceptor := chainStreamInterceptors(
		RequestIDStreamInterceptor(),
		LoggingStreamInterceptor(base),
		RecoveryStreamInterceptor(),
	)
	ss := &fakeServerStream{ctx: context.Background()}
	err := runStreamInterceptor(interceptor, ss, handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
	raw := buf.String()
	if !strings.Contains(raw, "grpc stream handler panicked") {
		t.Fatalf("expected panic log, got: %s", raw)
	}
	if !strings.Contains(raw, "stream-panic") {
		t.Fatalf("expected panic value in log, got: %s", raw)
	}
}

func TestStreamChainMetricsCountPanic(t *testing.T) {
	var buf bytes.Buffer
	base := testLogger(&buf)
	m := testRED(t)
	handler := func(srv any, ss grpc.ServerStream) error {
		panic("stream-chain-panic")
	}
	interceptor := chainStreamInterceptors(
		RequestIDStreamInterceptor(),
		LoggingStreamInterceptor(base),
		MetricsStreamInterceptor(m),
		RecoveryStreamInterceptor(),
	)
	ss := &fakeServerStream{ctx: context.Background()}
	err := runStreamInterceptor(interceptor, ss, handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
	if testutil.ToFloat64(m.RequestsTotal.WithLabelValues(testMethod, "Internal")) != 1 {
		t.Fatal("RequestsTotal after stream panic != 1")
	}
}

func TestRequestIDStreamClientPropagates(t *testing.T) {
	ctx := logger.WithRequestID(context.Background(), "client-stream-1")
	var gotMD metadata.MD
	streamer := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("expected outgoing metadata")
		}
		gotMD = md
		return nil, nil
	}
	_, err := RequestIDStreamClientInterceptor()(ctx, &grpc.StreamDesc{}, nil, testMethod, streamer)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	vals := gotMD.Get("x-request-id")
	if len(vals) != 1 || vals[0] != "client-stream-1" {
		t.Fatalf("x-request-id = %v, want [client-stream-1]", vals)
	}
}
