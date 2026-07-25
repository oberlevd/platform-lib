package healthcheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLivezAlwaysOK(t *testing.T) {
	h := New()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	h.LivezHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyzWithNoCheckersIsOK(t *testing.T) {
	h := New()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyzFailsWhenCheckerFails(t *testing.T) {
	h := New()
	h.Register("mssql-orders-01", func(ctx context.Context) error {
		return errors.New("connection refused")
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !contains(body, "connection refused") {
		t.Errorf("body should mention the failing check's error, got: %s", body)
	}
}

func TestReadyzOKWhenAllCheckersPass(t *testing.T) {
	h := New()
	h.Register("mssql-orders-01", func(ctx context.Context) error { return nil })
	h.Register("bus", func(ctx context.Context) error { return nil })

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.ReadyzHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

func TestRegisterHTTPMountsBothRoutes(t *testing.T) {
	h := New()
	mux := http.NewServeMux()
	h.RegisterHTTP(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
