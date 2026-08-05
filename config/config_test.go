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

type testConfigRedact struct {
	MSSQLHost     string `env:"TEST_REDACT_HOST"`
	MSSQLPassword string `env:"TEST_REDACT_PASSWORD" redact:"true"`
	HTTPPort      int    `env:"TEST_REDACT_PORT" default:"8080"`
}

func TestRedactedMasksTaggedFields(t *testing.T) {
	t.Setenv("TEST_REDACT_HOST", "mssql-orders-01")
	t.Setenv("TEST_REDACT_PASSWORD", "hunter2")

	var cfg testConfigRedact
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	out := Redacted(&cfg)

	if out["TEST_REDACT_HOST"] != "mssql-orders-01" {
		t.Errorf("non-redacted field = %q, want mssql-orders-01", out["TEST_REDACT_HOST"])
	}
	if out["TEST_REDACT_PASSWORD"] != redactedPlaceholder {
		t.Errorf("redacted field = %q, want placeholder %q", out["TEST_REDACT_PASSWORD"], redactedPlaceholder)
	}
	if out["TEST_REDACT_PASSWORD"] == "hunter2" {
		t.Fatal("Redacted() leaked the actual password value")
	}
	if out["TEST_REDACT_PORT"] != "8080" {
		t.Errorf("HTTPPort in Redacted() = %q, want 8080 (default)", out["TEST_REDACT_PORT"])
	}
}

func TestRedactedHandlesNilPointer(t *testing.T) {
	var cfg *testConfigRedact
	out := Redacted(cfg)
	if len(out) != 0 {
		t.Errorf("expected empty map for nil pointer, got %v", out)
	}
}

func TestRedactedIgnoresFieldsWithoutEnvTag(t *testing.T) {
	type withUntagged struct {
		Tracked   string `env:"TEST_REDACT_TRACKED"`
		Untracked string // без env-тега и не структура - не должно попасть в вывод
	}
	c := withUntagged{Tracked: "a", Untracked: "b"}

	out := Redacted(&c)
	if _, ok := out["Untracked"]; ok {
		t.Error("field without env tag leaked into Redacted() output")
	}
}

// --- Тесты на рекурсию во вложенные структуры (баг, который был найден
// и исправлен: раньше Load полностью игнорировал такие поля). ---

type nestedDBConfig struct {
	Host     string `env:"TEST_NESTED_DB_HOST,required"`
	Password string `env:"TEST_NESTED_DB_PASSWORD,required" redact:"true"`
	Port     int    `env:"TEST_NESTED_DB_PORT" default:"1433"`
}

type outerConfigWithNested struct {
	ServiceName string         `env:"TEST_OUTER_SERVICE_NAME" default:"svc"`
	DB          nestedDBConfig // без своего env-тега - должно обработаться рекурсивно
}

func TestLoadRecursesIntoNestedStruct(t *testing.T) {
	t.Setenv("TEST_NESTED_DB_HOST", "mssql-orders-01")
	t.Setenv("TEST_NESTED_DB_PASSWORD", "hunter2")

	var cfg outerConfigWithNested
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.DB.Host != "mssql-orders-01" {
		t.Errorf("DB.Host = %q, want mssql-orders-01 (nested struct was not populated)", cfg.DB.Host)
	}
	if cfg.DB.Port != 1433 {
		t.Errorf("DB.Port = %d, want default 1433", cfg.DB.Port)
	}
	if cfg.ServiceName != "svc" {
		t.Errorf("ServiceName = %q, want default svc", cfg.ServiceName)
	}
}

func TestLoadNestedStructRequiredFieldMissing(t *testing.T) {
	// TEST_NESTED_DB_HOST/PASSWORD не заданы - required-поле внутри
	// вложенной структуры должно всё равно приводить к ошибке Load.
	var cfg outerConfigWithNested
	err := Load(&cfg)
	if err == nil {
		t.Fatal("expected error for missing required field inside nested struct, got nil")
	}
}

func TestRedactedRecursesIntoNestedStruct(t *testing.T) {
	t.Setenv("TEST_NESTED_DB_HOST", "mssql-orders-01")
	t.Setenv("TEST_NESTED_DB_PASSWORD", "hunter2")

	var cfg outerConfigWithNested
	if err := Load(&cfg); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	out := Redacted(&cfg)

	if out["TEST_NESTED_DB_HOST"] != "mssql-orders-01" {
		t.Errorf("nested non-redacted field = %q, want mssql-orders-01", out["TEST_NESTED_DB_HOST"])
	}
	if out["TEST_NESTED_DB_PASSWORD"] != redactedPlaceholder {
		t.Errorf("nested redacted field = %q, want placeholder", out["TEST_NESTED_DB_PASSWORD"])
	}
	if out["TEST_NESTED_DB_PASSWORD"] == "hunter2" {
		t.Fatal("Redacted() leaked the nested password value")
	}
}
