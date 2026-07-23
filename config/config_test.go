package config

import (
	"testing"
	"time"
)

type testConfig struct {
	MSSQLHost      string        `env:"TEST_MSSQL_HOST,required"`
	MSSQLPassword  string        `env:"TEST_MSSQL_PASSWORD,required"`
	HTTPPort       int           `env:"TEST_HTTP_PORT" default:"8080"`
	RequestTimeout time.Duration `env:"TEST_REQUEST_TIMEOUT" default:"5s"`
	Debug          bool          `env:"TEST_DEBUG" default:"false"`
	Unset          string        `env:"TEST_UNSET_OPTIONAL"`
}

func TestLoadRequiredAndDefaults(t *testing.T) {
	t.Setenv("TEST_MSSQL_HOST", "mssql-orders-01")
	t.Setenv("TEST_MSSQL_PASSWORD", "hunter2")

	var cfg testConfig
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.MSSQLHost != "mssql-orders-01" {
		t.Errorf("MSSQLHost = %q, want %q", cfg.MSSQLHost, "mssql-orders-01")
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("HTTPPort = %d, want default 8080", cfg.HTTPPort)
	}
	if cfg.RequestTimeout != 5*time.Second {
		t.Errorf("RequestTimeout = %v, want default 5s", cfg.RequestTimeout)
	}
	if cfg.Debug != false {
		t.Errorf("Debug = %v, want default false", cfg.Debug)
	}
	if cfg.Unset != "" {
		t.Errorf("Unset = %q, want empty (no default, not present)", cfg.Unset)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	var cfg testConfig
	err := Load(&cfg)
	if err == nil {
		t.Fatal("expected error for missing required env var, got nil")
	}
}

func TestLoadOverridesDefault(t *testing.T) {
	t.Setenv("TEST_MSSQL_HOST", "h")
	t.Setenv("TEST_MSSQL_PASSWORD", "p")
	t.Setenv("TEST_HTTP_PORT", "9090")
	t.Setenv("TEST_DEBUG", "true")
	t.Setenv("TEST_REQUEST_TIMEOUT", "250ms")

	var cfg testConfig
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.HTTPPort != 9090 {
		t.Errorf("HTTPPort = %d, want 9090", cfg.HTTPPort)
	}
	if cfg.Debug != true {
		t.Errorf("Debug = %v, want true", cfg.Debug)
	}
	if cfg.RequestTimeout != 250*time.Millisecond {
		t.Errorf("RequestTimeout = %v, want 250ms", cfg.RequestTimeout)
	}
}

func TestLoadRejectsNonPointer(t *testing.T) {
	var cfg testConfig
	err := Load(cfg) // передали значение, а не указатель
	if err == nil {
		t.Fatal("expected error when passing non-pointer, got nil")
	}
}

func TestLoadInvalidIntValue(t *testing.T) {
	t.Setenv("TEST_MSSQL_HOST", "h")
	t.Setenv("TEST_MSSQL_PASSWORD", "p")
	t.Setenv("TEST_HTTP_PORT", "not-a-number")

	var cfg testConfig
	err := Load(&cfg)
	if err == nil {
		t.Fatal("expected error for invalid int env value, got nil")
	}
}

type testConfigJSON struct {
	Routes map[string]string `env:"TEST_MSSQL_ROUTES" env_json:"true"`
	Ports  []int             `env:"TEST_EXTRA_PORTS" env_json:"true"`
}

func TestLoadJSONMapField(t *testing.T) {
	t.Setenv("TEST_MSSQL_ROUTES", `{"orders":"mssql-orders-01","billing":"mssql-billing-02"}`)

	var cfg testConfigJSON
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Routes["orders"] != "mssql-orders-01" {
		t.Errorf("Routes[orders] = %q, want mssql-orders-01", cfg.Routes["orders"])
	}
	if cfg.Routes["billing"] != "mssql-billing-02" {
		t.Errorf("Routes[billing] = %q, want mssql-billing-02", cfg.Routes["billing"])
	}
}

func TestLoadJSONSliceField(t *testing.T) {
	t.Setenv("TEST_EXTRA_PORTS", `[8081, 8082, 8083]`)

	var cfg testConfigJSON
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := []int{8081, 8082, 8083}
	if len(cfg.Ports) != len(want) {
		t.Fatalf("Ports = %v, want %v", cfg.Ports, want)
	}
	for i := range want {
		if cfg.Ports[i] != want[i] {
			t.Errorf("Ports[%d] = %d, want %d", i, cfg.Ports[i], want[i])
		}
	}
}

func TestLoadJSONFieldInvalidJSON(t *testing.T) {
	t.Setenv("TEST_MSSQL_ROUTES", `not-json`)

	var cfg testConfigJSON
	err := Load(&cfg)
	if err == nil {
		t.Fatal("expected error for invalid JSON in env_json field, got nil")
	}
}
