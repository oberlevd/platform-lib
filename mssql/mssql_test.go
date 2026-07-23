package mssql

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Host:            "mssql-orders-01",
		Port:            1433,
		User:            "sa",
		Password:        "hunter2",
		Database:        "orders",
		MaxOpenConns:    20,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnectTimeout:  5 * time.Second,
	}
}

func TestDSNContainsExpectedParts(t *testing.T) {
	cfg := testConfig()
	dsn := cfg.dsn()

	for _, want := range []string{
		"sqlserver://",
		"sa:hunter2@",
		"mssql-orders-01:1433",
		"database=orders",
	} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn() = %q, expected to contain %q", dsn, want)
		}
	}
}

func TestSafeDSNRedactsPassword(t *testing.T) {
	cfg := testConfig()
	safe := cfg.SafeDSN()

	if strings.Contains(safe, "hunter2") {
		t.Errorf("SafeDSN() leaked the password: %q", safe)
	}
	if !strings.Contains(safe, "REDACTED") {
		t.Errorf("SafeDSN() = %q, expected redaction placeholder (maybe encoded)", safe)
	}
	// Остальная часть DSN должна остаться читаемой для дебага.
	if !strings.Contains(safe, "mssql-orders-01:1433") {
		t.Errorf("SafeDSN() = %q, expected host:port to remain visible", safe)
	}
	if !strings.Contains(safe, "database=orders") {
		t.Errorf("SafeDSN() = %q, expected database name to remain visible", safe)
	}
}

func TestOpenFailsFastOnUnreachableHost(t *testing.T) {
	cfg := testConfig()
	cfg.Host = "invalid.invalid" // зарезервированный несуществующий домен (RFC 2606)
	cfg.ConnectTimeout = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	db, err := Open(ctx, cfg)
	if err == nil {
		t.Fatal("expected error opening connection to an unreachable host, got nil")
	}
	if db != nil {
		t.Error("expected nil *sql.DB on error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Open error leaked the password: %v", err)
	}
}

func TestCheckerReturnsPingError(t *testing.T) {
	cfg := testConfig()
	cfg.Host = "invalid.invalid"
	cfg.ConnectTimeout = 200 * time.Millisecond

	// sql.Open сам по себе не коннектится — можно получить *sql.DB даже
	// для недоступного хоста и проверить, что Checker транслирует ошибку
	// именно через PingContext.
	db, err := sqlOpenOnly(cfg)
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	checker := Checker(db)
	if err := checker(ctx); err == nil {
		t.Fatal("expected Checker to fail for an unreachable host, got nil")
	}
}

// sqlOpenOnly — тестовый хелпер, открывающий *sql.DB без PingContext
// (в отличие от Open), чтобы отдельно проверить поведение Checker.
func sqlOpenOnly(cfg Config) (*sql.DB, error) {
	return sql.Open("sqlserver", cfg.dsn())
}
