package healthcheck

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const healthBufSize = 1024 * 1024

func startHealthGRPC(t *testing.T, h *Handler) (grpc_health_v1.HealthClient, func()) {
	t.Helper()
	lis := bufconn.Listen(healthBufSize)
	srv := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, NewGRPCServer(h))
	go func() {
		_ = srv.Serve(lis)
	}()
	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	cancel()
	if err != nil {
		srv.Stop()
		t.Fatalf("dial: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return grpc_health_v1.NewHealthClient(conn), cleanup
}

func TestGRPCCheckServingWhenNoCheckers(t *testing.T) {
	h := New()
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.Status)
	}
}

func TestGRPCCheckServingWhenAllPass(t *testing.T) {
	h := New()
	h.Register("db", func(ctx context.Context) error { return nil })
	h.Register("bus", func(ctx context.Context) error { return nil })
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.Status)
	}
}

func TestGRPCCheckNotServingWhenCheckerFails(t *testing.T) {
	h := New()
	h.Register("mssql", func(ctx context.Context) error {
		return errors.New("connection refused")
	})
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", resp.Status)
	}
}

func TestGRPCCheckIgnoresServiceName(t *testing.T) {
	h := New()
	h.Register("db", func(ctx context.Context) error { return nil })
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{Service: "orders.OrderService"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", resp.Status)
	}
}

func TestGRPCWatchUnimplemented(t *testing.T) {
	h := New()
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	stream, err := client.Watch(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Watch dial: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("Watch Recv code = %v, want Unimplemented, err=%v", status.Code(err), err)
	}
}

func TestGRPCCheckRespectsTimeout(t *testing.T) {
	h := New(WithCheckTimeout(50 * time.Millisecond))
	h.Register("slow", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	})
	client, cleanup := startHealthGRPC(t, h)
	defer cleanup()
	start := time.Now()
	resp, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", resp.Status)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Check took %v, expected to fail fast under check timeout", elapsed)
	}
}
