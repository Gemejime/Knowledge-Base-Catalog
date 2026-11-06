package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseEnvReader(t *testing.T) {
	input := `
# System configuration
ENVIRONMENT=production
LOG_LEVEL=debug

# Network ports
HTTP_PORT=8081
GRPC_PORT=50052
METRICS_PORT=9091

# Quoted values and escape handling
JWT_HMAC_SECRET="my-super-secret-key-that-is-at-least-32-bytes"
SPECIAL_VALUE='quoted single string'
ESCAPED_STRING="line1\nline2\t\"quoted\""

# Export keyword
export REDIS_PASSWORD=secret_redis_pass

# Inline comment
INLINE_COMMENT_TEST=value_here # this is an inline comment
DATABASE_URL=postgres://user:pass@localhost:5432/rag_catalog?sslmode=disable
`

	vars, err := ParseEnvReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error parsing env: %v", err)
	}

	expected := map[string]string{
		"ENVIRONMENT":         "production",
		"LOG_LEVEL":           "debug",
		"HTTP_PORT":           "8081",
		"GRPC_PORT":           "50052",
		"METRICS_PORT":        "9091",
		"JWT_HMAC_SECRET":     "my-super-secret-key-that-is-at-least-32-bytes",
		"SPECIAL_VALUE":       "quoted single string",
		"ESCAPED_STRING":      "line1\nline2\t\"quoted\"",
		"REDIS_PASSWORD":      "secret_redis_pass",
		"INLINE_COMMENT_TEST": "value_here",
		"DATABASE_URL":        "postgres://user:pass@localhost:5432/rag_catalog?sslmode=disable",
	}

	for k, expVal := range expected {
		if got := vars[k]; got != expVal {
			t.Errorf("key %q: expected %q, got %q", k, expVal, got)
		}
	}
}

func TestLoadEnvFile_ChangesAffectEnvironment(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")

	// Initial version of .env
	initialContent := `
HTTP_PORT=9001
JWT_HMAC_SECRET="initial-jwt-secret-at-least-32-bytes"
LOG_LEVEL=warn
`
	if err := os.WriteFile(envPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("failed to write initial .env: %v", err)
	}

	// Clean up environment variables after test
	t.Cleanup(func() {
		_ = os.Unsetenv("HTTP_PORT")
		_ = os.Unsetenv("HTTP_ADDR")
		_ = os.Unsetenv("JWT_HMAC_SECRET")
		_ = os.Unsetenv("LOG_LEVEL")
	})

	if err := LoadEnv(envPath); err != nil {
		t.Fatalf("failed to load initial .env: %v", err)
	}

	cfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("failed to parse initial config: %v", err)
	}
	if cfg.HTTPAddr != ":9001" {
		t.Errorf("expected HTTPAddr :9001, got %s", cfg.HTTPAddr)
	}
	if cfg.LogLevel != slog.LevelWarn {
		t.Errorf("expected LogLevel LevelWarn, got %v", cfg.LogLevel)
	}
	if cfg.JWTSecret != "initial-jwt-secret-at-least-32-bytes" {
		t.Errorf("unexpected initial JWTSecret: %s", cfg.JWTSecret)
	}

	// Change value of [.env]
	updatedContent := `
HTTP_PORT=9099
JWT_HMAC_SECRET="updated-jwt-secret-with-more-than-32-bytes"
LOG_LEVEL=debug
`
	if err := os.WriteFile(envPath, []byte(updatedContent), 0600); err != nil {
		t.Fatalf("failed to write updated .env: %v", err)
	}

	// Re-load and verify change in [.env] affects the project configuration
	if err := LoadEnv(envPath); err != nil {
		t.Fatalf("failed to load updated .env: %v", err)
	}

	updatedCfg, err := Load(envPath)
	if err != nil {
		t.Fatalf("failed to parse updated config: %v", err)
	}
	if updatedCfg.HTTPAddr != ":9099" {
		t.Errorf("expected updated HTTPAddr :9099, got %s", updatedCfg.HTTPAddr)
	}
	if updatedCfg.LogLevel != slog.LevelDebug {
		t.Errorf("expected updated LogLevel LevelDebug, got %v", updatedCfg.LogLevel)
	}
	if updatedCfg.JWTSecret != "updated-jwt-secret-with-more-than-32-bytes" {
		t.Errorf("unexpected updated JWTSecret: %s", updatedCfg.JWTSecret)
	}
	if os.Getenv("HTTP_PORT") != "9099" {
		t.Errorf("os.Getenv(HTTP_PORT) was not updated: %s", os.Getenv("HTTP_PORT"))
	}
}

func TestLoad_ValidationAndDefaults(t *testing.T) {
	// 1. Missing JWT Secret
	_ = os.Unsetenv("JWT_HMAC_SECRET")
	_ = os.Unsetenv("HTTP_ADDR")
	_ = os.Unsetenv("HTTP_PORT")
	_ = os.Unsetenv("DATABASE_URL")
	_ = os.Unsetenv("POSTGRES_HOST")

	emptyEnv := filepath.Join(t.TempDir(), "empty.env")
	if err := os.WriteFile(emptyEnv, []byte{}, 0600); err != nil {
		t.Fatalf("failed to create empty env: %v", err)
	}

	_, err := Load("non-existent-file-path-triggers-nothing")
	if err == nil {
		t.Fatal("expected error loading explicitly named nonexistent file, got nil")
	}
	// Call Load without env file
	t.Setenv("JWT_HMAC_SECRET", "")
	_, err = Load(emptyEnv)
	if !errors.Is(err, ErrMissingJWTSecret) {
		t.Fatalf("expected ErrMissingJWTSecret, got %v", err)
	}

	// Secret shorter than 32 bytes
	t.Setenv("JWT_HMAC_SECRET", "too-short")
	_, err = Load(emptyEnv)
	if !errors.Is(err, ErrJWTSecretTooShort) {
		t.Fatalf("expected ErrJWTSecretTooShort, got %v", err)
	}

	// Host-only invalid address
	t.Setenv("JWT_HMAC_SECRET", "super-secret-jwt-key-minimum-32-chars-long")
	t.Setenv("HTTP_ADDR", "localhost")
	_, err = Load(emptyEnv)
	if err == nil {
		t.Fatal("expected error for host-only HTTP_ADDR without port, got nil")
	}
	_ = os.Unsetenv("HTTP_ADDR")

	// Out-of-range POSTGRES_MAX_CONNS preserves default
	t.Setenv("POSTGRES_MAX_CONNS", "99999999999999999999")
	cfgMax, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error loading with overflow conn count: %v", err)
	}
	if cfgMax.Postgres.MaxConns != 25 {
		t.Fatalf("expected default MaxConns 25 on overflow, got %d", cfgMax.Postgres.MaxConns)
	}
	_ = os.Unsetenv("POSTGRES_MAX_CONNS")

	// 2. Default addresses and levels
	t.Setenv("JWT_HMAC_SECRET", "super-secret-jwt-key-minimum-32-chars-long")
	t.Setenv("LOG_LEVEL", "")
	_ = os.Unsetenv("HTTP_ADDR")
	_ = os.Unsetenv("HTTP_PORT")
	_ = os.Unsetenv("PORT")
	cfg, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("expected default HTTPAddr :8080, got %s", cfg.HTTPAddr)
	}
	if cfg.GRPCAddr != ":50051" {
		t.Errorf("expected default GRPCAddr :50051, got %s", cfg.GRPCAddr)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Errorf("expected default MetricsAddr :9090, got %s", cfg.MetricsAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("expected default LogLevel LevelInfo, got %v", cfg.LogLevel)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected default Environment development, got %s", cfg.Environment)
	}

	// Fallback to PORT if HTTP_ADDR and HTTP_PORT are empty
	t.Setenv("PORT", "8888")
	cfgPort, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error loading config with PORT: %v", err)
	}
	if cfgPort.HTTPAddr != ":8888" {
		t.Errorf("expected HTTPAddr :8888 from PORT, got %s", cfgPort.HTTPAddr)
	}
	_ = os.Unsetenv("PORT")

	// 3. Postgres DSN assembly from parts
	t.Setenv("POSTGRES_HOST", "db.example.com")
	t.Setenv("POSTGRES_PORT", "5433")
	t.Setenv("POSTGRES_USER", "catalog_usr")
	t.Setenv("POSTGRES_PASSWORD", "catalog_pwd")
	t.Setenv("POSTGRES_DB", "kb_catalog")
	t.Setenv("POSTGRES_SSLMODE", "require")
	t.Setenv("POSTGRES_MAX_CONNS", "40")
	t.Setenv("POSTGRES_MIN_CONNS", "15")
	t.Setenv("POSTGRES_MAX_CONN_LIFETIME", "15m")
	t.Setenv("POSTGRES_MAX_CONN_IDLE_TIME", "2m")

	cfgWithPg, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedDSN := "postgres://catalog_usr:catalog_pwd@db.example.com:5433/kb_catalog?sslmode=require"
	if cfgWithPg.Postgres.DSN != expectedDSN {
		t.Errorf("expected DSN %q, got %q", expectedDSN, cfgWithPg.Postgres.DSN)
	}
	if cfgWithPg.Postgres.MaxConns != 40 {
		t.Errorf("expected MaxConns 40, got %d", cfgWithPg.Postgres.MaxConns)
	}
	if cfgWithPg.Postgres.MinConns != 15 {
		t.Errorf("expected MinConns 15, got %d", cfgWithPg.Postgres.MinConns)
	}
	if cfgWithPg.Postgres.MaxConnLifetime != 15*time.Minute {
		t.Errorf("expected MaxConnLifetime 15m, got %v", cfgWithPg.Postgres.MaxConnLifetime)
	}
	if cfgWithPg.Postgres.MaxConnIdleTime != 2*time.Minute {
		t.Errorf("expected MaxConnIdleTime 2m, got %v", cfgWithPg.Postgres.MaxConnIdleTime)
	}

	poolCfg := cfgWithPg.Postgres.PoolConfig()
	if poolCfg.DSN != expectedDSN || poolCfg.MaxConns != 40 || poolCfg.MinConns != 15 {
		t.Errorf("PoolConfig() did not propagate properties correctly: %+v", poolCfg)
	}

	// 4. DATABASE_URL takes precedence over modular variables
	t.Setenv("DATABASE_URL", "postgres://override_user:override_pass@pgbouncer:6432/override_db?sslmode=verify-full")
	cfgOverride, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfgOverride.Postgres.DSN != "postgres://override_user:override_pass@pgbouncer:6432/override_db?sslmode=verify-full" {
		t.Errorf("expected DATABASE_URL precedence, got %s", cfgOverride.Postgres.DSN)
	}

	// 5. Redis cluster addresses parsing
	t.Setenv("REDIS_CLUSTER_ADDRS", "node1:6379, node2:6379,node3:6379 ")
	t.Setenv("REDIS_PASSWORD", "redis_secret")
	cfgRedis, err := Load(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgRedis.Redis.Addrs) != 3 || cfgRedis.Redis.Addrs[0] != "node1:6379" || cfgRedis.Redis.Addrs[1] != "node2:6379" || cfgRedis.Redis.Addrs[2] != "node3:6379" {
		t.Errorf("unexpected Redis addrs: %+v", cfgRedis.Redis.Addrs)
	}
	if cfgRedis.Redis.Password != "redis_secret" {
		t.Errorf("unexpected Redis password: %s", cfgRedis.Redis.Password)
	}
}
