package mongo

import (
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Host:           "mongo-sessions-01",
		Port:           27017,
		User:           "admin",
		Password:       "hunter2",
		AuthDB:         "my_auth_db",
		MaxPoolSize:    100,
		MinPoolSize:    0,
		ConnectTimeout: 5 * time.Second,
		// RetryAttempts/RetryBaseDelay/RetryMaxDelay намеренно не
		// заданы в большинстве тестов - effectiveRetryAttempts()
		// падает на 1 попытку, сохраняя старое быстрое поведение
		// тестов, которым retry не нужен.
	}
}

func TestURIContainsExpectedParts(t *testing.T) {
	cfg := testConfig()
	uri := cfg.uri()

	for _, want := range []string{
		"mongodb://",
		"admin:hunter2@",
		"mongo-sessions-01:27017",
		"authSource=my_auth_db",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri() = %q, expected to contain %q", uri, want)
		}
	}
}

func TestSafeURIRedactsPassword(t *testing.T) {
	cfg := testConfig()
	safe := cfg.SafeURI()

	if strings.Contains(safe, "hunter2") {
		t.Errorf("SafeURI() leaked the password: %q", safe)
	}
	if !strings.Contains(safe, "REDACTED") {
		t.Errorf("SafeURI() = %q, expected redaction placeholder (maybe encoded)", safe)
	}
	if !strings.Contains(safe, "mongo-sessions-01:27017") {
		t.Errorf("SafeURI() = %q, expected host:port to remain visible", safe)
	}
	if !strings.Contains(safe, "authSource=my_auth_db") {
		t.Errorf("SafeURI() = %q, expected authSource to remain visible", safe)
	}
}

func TestEffectiveDatabaseFallsBackToAuthDB(t *testing.T) {
	cfg := testConfig()

	if got := cfg.effectiveDatabase(); got != cfg.AuthDB {
		t.Errorf("effectiveDatabase() = %q, want fallback to AuthDB %q", got, cfg.AuthDB)
	}

	cfg.Database = "sessions_db"
	if got := cfg.effectiveDatabase(); got != "sessions_db" {
		t.Errorf("effectiveDatabase() = %q, want explicit Database %q", got, "sessions_db")
	}
}

func TestEffectiveRetryAttemptsDefaultsToOneWithoutConfigLoad(t *testing.T) {
	cfg := testConfig() // RetryAttempts не задан
	if got := cfg.effectiveRetryAttempts(); got != 1 {
		t.Errorf("effectiveRetryAttempts() = %d, want 1 for zero-value Config literal", got)
	}
}
