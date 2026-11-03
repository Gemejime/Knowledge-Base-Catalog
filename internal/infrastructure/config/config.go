package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
)

var (
	// ErrMissingJWTSecret is returned when JWT_HMAC_SECRET is missing or whitespace only.
	ErrMissingJWTSecret = errors.New("JWT_HMAC_SECRET environment variable is required and cannot be empty")
)

// PostgresConfig holds PostgreSQL connection and pool parameters.
type PostgresConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// PoolConfig converts PostgresConfig into the postgres.PoolConfig struct.
func (p PostgresConfig) PoolConfig() postgres.PoolConfig {
	return postgres.PoolConfig{
		DSN:             p.DSN,
		MaxConns:        p.MaxConns,
		MinConns:        p.MinConns,
		MaxConnLifetime: p.MaxConnLifetime,
		MaxConnIdleTime: p.MaxConnIdleTime,
	}
}

// RedisConfig holds Redis 7.2 Cluster connectivity settings.
type RedisConfig struct {
	Addrs    []string
	Password string
}

// TemporalConfig holds Temporal orchestrator connectivity settings.
type TemporalConfig struct {
	HostPort  string
	Namespace string
	TaskQueue string
}

// Config represents the complete runtime configuration of the catalog service.
type Config struct {
	Environment string
	LogLevel    slog.Level
	HTTPAddr    string
	GRPCAddr    string
	MetricsAddr string
	JWTSecret   string
	Postgres    PostgresConfig
	Redis       RedisConfig
	Temporal    TemporalConfig
}

// LoadEnv attempts to locate and parse .env files, setting key-value pairs into os.Setenv.
// If filenames are provided, each specified file is loaded in order.
// If no filenames are provided, it looks for .env in the current working directory,
// walking upwards to find the repository root (marked by go.mod or .git) if not in the immediate directory.
// If no .env file is found when searching default locations, no error is returned (allowing containerized execution).
func LoadEnv(filenames ...string) error {
	if len(filenames) > 0 {
		for _, f := range filenames {
			if err := loadSingleEnvFile(f, true); err != nil {
				return err
			}
		}
		return nil
	}

	target := findEnvFile()
	if target == "" {
		return nil
	}

	return loadSingleEnvFile(target, false)
}

// findEnvFile searches for a .env file in the current working directory and parent directories.
func findEnvFile() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		candidate := filepath.Join(dir, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}

		// Stop if we hit repository or volume root
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// Checked go.mod dir; if .env wasn't there, stop
			break
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

// loadSingleEnvFile reads a single .env file and sets values into the process environment.
func loadSingleEnvFile(path string, required bool) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		return fmt.Errorf("failed to open env file %s: %w", path, err)
	}
	defer file.Close()

	vars, err := ParseEnvReader(file)
	if err != nil {
		return fmt.Errorf("failed to parse env file %s: %w", path, err)
	}

	for k, v := range vars {
		_ = os.Setenv(k, v)
	}

	return nil
}

// ParseEnvReader parses KEY=VALUE statements from an io.Reader according to POSIX / dotenv standards.
func ParseEnvReader(r io.Reader) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(r)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Support "export KEY=VALUE"
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		eqIdx := strings.IndexByte(line, '=')
		if eqIdx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:eqIdx])
		if key == "" {
			continue
		}

		val := strings.TrimSpace(line[eqIdx+1:])
		val = parseEnvValue(val)

		result[key] = val
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

// parseEnvValue extracts and unquotes a dotenv value string.
func parseEnvValue(raw string) string {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			quote := raw[0]
			unquoted := raw[1 : len(raw)-1]
			if quote == '"' {
				unquoted = strings.ReplaceAll(unquoted, `\"`, `"`)
				unquoted = strings.ReplaceAll(unquoted, `\n`, "\n")
				unquoted = strings.ReplaceAll(unquoted, `\r`, "\r")
				unquoted = strings.ReplaceAll(unquoted, `\t`, "\t")
				unquoted = strings.ReplaceAll(unquoted, `\\`, `\`)
			}
			return unquoted
		}
	}

	// For unquoted values, truncate trailing inline comments (# ...)
	if commentIdx := strings.Index(raw, " #"); commentIdx != -1 {
		raw = strings.TrimSpace(raw[:commentIdx])
	} else if strings.HasPrefix(raw, "#") {
		return ""
	}

	return raw
}

// Load loads any existing .env file into the environment and parses the Config.
func Load(envFiles ...string) (*Config, error) {
	if err := LoadEnv(envFiles...); err != nil {
		return nil, err
	}

	cfg := &Config{
		Environment: envOrDefault("ENVIRONMENT", "development"),
		LogLevel:    parseLogLevel(os.Getenv("LOG_LEVEL")),
		HTTPAddr:    normalizeAddr(envOrDefault("HTTP_ADDR", ""), envOrDefault("HTTP_PORT", ""), ":8080"),
		GRPCAddr:    normalizeAddr(envOrDefault("GRPC_ADDR", ""), envOrDefault("GRPC_PORT", ""), ":50051"),
		MetricsAddr: normalizeAddr(envOrDefault("METRICS_ADDR", ""), envOrDefault("METRICS_PORT", ""), ":9090"),
		JWTSecret:   os.Getenv("JWT_HMAC_SECRET"),
		Postgres:    parsePostgresConfig(),
		Redis:       parseRedisConfig(),
		Temporal:    parseTemporalConfig(),
	}

	if strings.TrimSpace(cfg.JWTSecret) == "" {
		return nil, ErrMissingJWTSecret
	}

	return cfg, nil
}

func parseLogLevel(lvl string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(lvl)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func normalizeAddr(addrVal, portVal, fallback string) string {
	if strings.TrimSpace(addrVal) != "" {
		trimmed := strings.TrimSpace(addrVal)
		if !strings.Contains(trimmed, ":") {
			return ":" + trimmed
		}
		return trimmed
	}
	if strings.TrimSpace(portVal) != "" {
		trimmed := strings.TrimSpace(portVal)
		if strings.HasPrefix(trimmed, ":") {
			return trimmed
		}
		return ":" + trimmed
	}
	return fallback
}

func parsePostgresConfig() PostgresConfig {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		host := strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
		if host != "" {
			port := envOrDefault("POSTGRES_PORT", "5432")
			user := envOrDefault("POSTGRES_USER", "postgres")
			password := os.Getenv("POSTGRES_PASSWORD")
			db := envOrDefault("POSTGRES_DB", "rag_catalog")
			sslMode := envOrDefault("POSTGRES_SSLMODE", "disable")

			var userInfo *url.Userinfo
			if password != "" {
				userInfo = url.UserPassword(user, password)
			} else {
				userInfo = url.User(user)
			}

			u := url.URL{
				Scheme: "postgres",
				User:   userInfo,
				Host:   net.JoinHostPort(host, port),
				Path:   db,
			}
			q := u.Query()
			q.Set("sslmode", sslMode)
			u.RawQuery = q.Encode()
			dsn = u.String()
		}
	}

	maxConns := int32(parseEnvInt("POSTGRES_MAX_CONNS", 25))
	minConns := int32(parseEnvInt("POSTGRES_MIN_CONNS", 5))
	maxLifetime := parseEnvDuration("POSTGRES_MAX_CONN_LIFETIME", 30*time.Minute)
	maxIdleTime := parseEnvDuration("POSTGRES_MAX_CONN_IDLE_TIME", 5*time.Minute)

	return PostgresConfig{
		DSN:             dsn,
		MaxConns:        maxConns,
		MinConns:        minConns,
		MaxConnLifetime: maxLifetime,
		MaxConnIdleTime: maxIdleTime,
	}
}

func parseRedisConfig() RedisConfig {
	rawAddrs := os.Getenv("REDIS_CLUSTER_ADDRS")
	password := os.Getenv("REDIS_PASSWORD")

	var addrs []string
	if strings.TrimSpace(rawAddrs) != "" {
		for _, addr := range strings.Split(rawAddrs, ",") {
			trimmed := strings.TrimSpace(addr)
			if trimmed != "" {
				addrs = append(addrs, trimmed)
			}
		}
	}

	return RedisConfig{
		Addrs:    addrs,
		Password: password,
	}
}

func parseTemporalConfig() TemporalConfig {
	return TemporalConfig{
		HostPort:  strings.TrimSpace(os.Getenv("TEMPORAL_HOST_PORT")),
		Namespace: envOrDefault("TEMPORAL_NAMESPACE", "default"),
		TaskQueue: envOrDefault("TEMPORAL_TASK_QUEUE", "rag-ingestion-backfill"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseEnvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func parseEnvDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
