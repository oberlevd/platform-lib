package lifecycle

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerShutdown(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := &http.Server{Handler: http.NewServeMux()}
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- srv.Serve(lis)
	}()

	// Даём серверу время фактически начать принимать соединения перед
	// тем, как его останавливать.
	time.Sleep(20 * time.Millisecond)

	shutdown := HTTPServerShutdown(srv)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := shutdown(ctx); err != nil {
		t.Errorf("expected nil error from HTTPServerShutdown, got %v", err)
	}

	if err := <-serveErrCh; err != http.ErrServerClosed {
		t.Errorf("Serve returned %v, want http.ErrServerClosed", err)
	}
}
