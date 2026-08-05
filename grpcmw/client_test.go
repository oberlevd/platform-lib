package grpcmw

import (
	"context"
	"testing"

	"github.com/oberlevd/platform-lib/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDUnaryClientInterceptorPropagatesFromContext(t *testing.T) {
	ctx := logger.WithRequestID(context.Background(), "client-req-1")
	var gotMD metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("expected outgoing metadata")
		}
		gotMD = md
		return nil
	}
	err := RequestIDUnaryClientInterceptor()(ctx, "/test.TestService/Ping", nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	vals := gotMD.Get("x-request-id")
	if len(vals) != 1 || vals[0] != "client-req-1" {
		t.Fatalf("x-request-id = %v, want [client-req-1]", vals)
	}
}

func TestRequestIDUnaryClientInterceptorGeneratesWhenMissing(t *testing.T) {
	var gotMD metadata.MD
	var gotCtxID string
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		gotCtxID = logger.RequestIDFromContext(ctx)
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("expected outgoing metadata")
		}
		gotMD = md
		return nil
	}
	err := RequestIDUnaryClientInterceptor()(context.Background(), "/test.TestService/Ping", nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	vals := gotMD.Get("x-request-id")
	if len(vals) != 1 {
		t.Fatalf("x-request-id = %v, want one value", vals)
	}
	if len(vals[0]) != 32 {
		t.Fatalf("generated id len = %d, want 32", len(vals[0]))
	}
	if gotCtxID != vals[0] {
		t.Fatalf("context request_id = %q, metadata = %q", gotCtxID, vals[0])
	}
}

func TestRequestIDUnaryClientInterceptorDoesNotOverwriteExistingMetadata(t *testing.T) {
	md := metadata.Pairs("x-request-id", "already-set")
	ctx := metadata.NewOutgoingContext(context.Background(), md)
	ctx = logger.WithRequestID(ctx, "from-context")
	var gotMD metadata.MD
	invoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		var ok bool
		gotMD, ok = metadata.FromOutgoingContext(ctx)
		if !ok {
			t.Fatal("expected outgoing metadata")
		}
		return nil
	}
	err := RequestIDUnaryClientInterceptor()(ctx, "/test.TestService/Ping", nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	vals := gotMD.Get("x-request-id")
	if len(vals) != 1 || vals[0] != "already-set" {
		t.Fatalf("x-request-id = %v, want [already-set]", vals)
	}
}

func TestClientChainDialOption(t *testing.T) {
	opt := ClientChain()
	if opt == nil {
		t.Fatal("ClientChain returned nil")
	}
}
