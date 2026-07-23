package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerBasicFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		Service: "svc-test",
		Version: "abc123",
		Env:     "test",
		Level:   slog.LevelInfo,
		Output:  &buf,
	})

	ctx := WithRequestID(context.Background(), "req-1")
	l.Info(ctx, "hello world", "user_id", 42)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}

	for _, field := range []string{"timestamp", "level", "service", "version", "env", "message"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("missing expected field %q in log entry: %v", field, entry)
		}
	}

	if entry["service"] != "svc-test" {
		t.Errorf("service = %v, want svc-test", entry["service"])
	}
	if entry["message"] != "hello world" {
		t.Errorf("message = %v, want %q", entry["message"], "hello world")
	}
	if entry["user_id"].(float64) != 42 {
		t.Errorf("user_id = %v, want 42", entry["user_id"])
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		Service:    "svc-test",
		Version:    "abc123",
		Env:        "test",
		Level:      slog.LevelInfo,
		Output:     &buf,
		RedactKeys: []string{"mssql_conn_string"},
	})

	ctx := context.Background()
	l.Info(ctx, "connecting to db",
		"password", "supersecret",
		"api_key", "sk-12345",
		"mssql_conn_string", "Server=host;Password=abc;",
		"host", "db-host-1", // не должно маскироваться
	)

	raw := buf.String()

	if strings.Contains(raw, "supersecret") {
		t.Errorf("password leaked into log output: %s", raw)
	}
	if strings.Contains(raw, "sk-12345") {
		t.Errorf("api_key leaked into log output: %s", raw)
	}
	if strings.Contains(raw, "Password=abc") {
		t.Errorf("mssql_conn_string leaked into log output: %s", raw)
	}
	if !strings.Contains(raw, "db-host-1") {
		t.Errorf("non-sensitive field 'host' was unexpectedly redacted: %s", raw)
	}
	if !strings.Contains(raw, redactedPlaceholder) {
		t.Errorf("expected redaction placeholder in output: %s", raw)
	}
}

func TestLoggerRedactsSecretsEmbeddedInValues(t *testing.T) {
	var buf bytes.Buffer
	l := New(Config{
		Service: "svc-test",
		Version: "abc123",
		Env:     "test",
		Level:   slog.LevelInfo,
		Output:  &buf,
	})

	ctx := context.Background()
	// Ключ "dsn" сам по себе не входит в redact-список — секрет
	// прячется внутри значения, а не в имени поля.
	l.Info(ctx, "mssql connection attempt",
		"dsn", "Server=mssql-orders-01;Password=abc123;User Id=sa;",
		"note", "just a normal string with no secrets in it",
	)

	raw := buf.String()

	if strings.Contains(raw, "Password=abc123") {
		t.Errorf("password embedded in a non-flagged field leaked into log output: %s", raw)
	}
	if !strings.Contains(raw, "Server=mssql-orders-01") {
		t.Errorf("non-sensitive part of the dsn value was unexpectedly redacted: %s", raw)
	}
	if !strings.Contains(raw, "User Id=sa") {
		t.Errorf("non-sensitive part of the dsn value was unexpectedly redacted: %s", raw)
	}
	if !strings.Contains(raw, "just a normal string with no secrets in it") {
		t.Errorf("benign string value was unexpectedly modified: %s", raw)
	}
}

func TestFromContextFallsBackToDefault(t *testing.T) {
	// Логгер не был положен в контекст — FromContext не должен паниковать
	// и должен вернуть рабочий default logger.
	l := FromContext(context.Background())
	if l == nil {
		t.Fatal("FromContext returned nil, expected default logger")
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	id := NewRequestID()
	if len(id) != 32 {
		t.Errorf("expected 32-char hex request id, got %q (len=%d)", id, len(id))
	}

	ctx := WithRequestID(context.Background(), id)
	got := RequestIDFromContext(ctx)
	if got != id {
		t.Errorf("RequestIDFromContext = %q, want %q", got, id)
	}

	// Пустой контекст без request_id.
	if empty := RequestIDFromContext(context.Background()); empty != "" {
		t.Errorf("expected empty string for context without request id, got %q", empty)
	}
}
