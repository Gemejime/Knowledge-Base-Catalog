package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

const (
	// DefaultTTL is the bounded safety TTL (1 hour / 3600s) for Redis Cluster cache keys (ADR-003).
	DefaultTTL = 3600 * time.Second

	// DefaultDialTimeout is the default timeout for establishing a connection to Redis Cluster.
	DefaultDialTimeout = 5 * time.Second

	// DefaultReadTimeout is the default socket read timeout.
	DefaultReadTimeout = 2 * time.Second

	// DefaultWriteTimeout is the default socket write timeout.
	DefaultWriteTimeout = 2 * time.Second
)

// advancePointerLua is the atomic Lua script that compares and conditionally advances
// the latest version pointer key if the incoming version is strictly greater than the current pointer.
var advancePointerLua = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if not cur or tonumber(ARGV[1]) > tonumber(cur) then
  redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
  return 1
end
return 0
`)

// VersionedKey generates the deterministic versioned payload key for a Knowledge Base.
// The {tenant_id} is wrapped in curly braces to guarantee that both the versioned payload key
// and latest version pointer key map to the exact same CRC16 hash slot in Redis 7.2 Cluster (ADR-003).
func VersionedKey(tenantID, kbID string, version uint64) string {
	return fmt.Sprintf("kb:{%s}:%s:v%d", strings.TrimSpace(tenantID), strings.TrimSpace(kbID), version)
}

// LatestKey generates the deterministic latest version pointer key for a Knowledge Base.
// The {tenant_id} is wrapped in curly braces to guarantee co-location with all versioned keys for the tenant.
func LatestKey(tenantID, kbID string) string {
	return fmt.Sprintf("kb:{%s}:%s:latest_version", strings.TrimSpace(tenantID), strings.TrimSpace(kbID))
}

// Config encapsulates configuration parameters for the Redis 7.2 Cluster client.
type Config struct {
	Addrs        []string
	Password     string
	TTL          time.Duration
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// ClusterClient wraps redis.ClusterClient to provide versioned caching conforming to ADR-003.
type ClusterClient struct {
	client *redis.ClusterClient
	ttl    time.Duration
	logger *slog.Logger
}

// NewClusterClient initializes and verifies a Redis 7.2 Cluster client connection.
func NewClusterClient(ctx context.Context, cfg Config, logger *slog.Logger) (*ClusterClient, error) {
	if len(cfg.Addrs) == 0 {
		return nil, errors.New("redis cluster addrs cannot be empty")
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}
	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = DefaultReadTimeout
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultWriteTimeout
	}

	if logger != nil {
		logger.Info("initializing redis 7.2 cluster client",
			slog.Any("addrs", cfg.Addrs),
			slog.Duration("ttl", ttl),
			slog.String("password", "[REDACTED]"),
		)
	}

	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        cfg.Addrs,
		Password:     cfg.Password,
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	})

	pingCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("failed to connect to redis cluster: %w", err)
	}

	return &ClusterClient{
		client: rdb,
		ttl:    ttl,
		logger: logger,
	}, nil
}

// PutVersion stores an immutable versioned Knowledge Base payload and advances the latest version pointer.
func (c *ClusterClient) PutVersion(ctx context.Context, kb *entity.KnowledgeBase) error {
	if kb == nil {
		return errors.New("cannot cache nil knowledge base")
	}
	tenantID := strings.TrimSpace(kb.TenantID)
	kbID := strings.TrimSpace(kb.KBID)
	if tenantID == "" || kbID == "" {
		return errors.New("tenant_id and kb_id are required for caching")
	}

	payload, err := json.Marshal(kb)
	if err != nil {
		return fmt.Errorf("failed to marshal knowledge base for cache: %w", err)
	}

	ttlSeconds := int64(c.ttl.Seconds())
	if ttlSeconds <= 0 {
		ttlSeconds = int64(DefaultTTL.Seconds())
	}

	vKey := VersionedKey(tenantID, kbID, kb.Version)
	lKey := LatestKey(tenantID, kbID)

	// 1. Store the immutable versioned payload
	if err := c.client.Set(ctx, vKey, payload, c.ttl).Err(); err != nil {
		return fmt.Errorf("failed to set versioned key %s: %w", vKey, err)
	}

	// 2. Monotonically advance the latest version pointer via Lua script
	if err := advancePointerLua.Run(ctx, c.client, []string{lKey}, kb.Version, ttlSeconds).Err(); err != nil {
		return fmt.Errorf("failed to advance latest pointer key %s: %w", lKey, err)
	}

	return nil
}

// GetLatest retrieves the latest versioned Knowledge Base snapshot from Redis Cluster.
// When minVersion > 0 and the cached version is strictly less than minVersion (or on a cache miss),
// it returns nil, nil to allow caller to fall back to PostgreSQL and maintain causal freshness (ADR-003).
func (c *ClusterClient) GetLatest(ctx context.Context, tenantID, kbID string, minVersion uint64) (*entity.KnowledgeBase, error) {
	tID := strings.TrimSpace(tenantID)
	kID := strings.TrimSpace(kbID)
	if tID == "" || kID == "" {
		return nil, errors.New("tenant_id and kb_id are required for cache lookup")
	}

	lKey := LatestKey(tID, kID)
	val, err := c.client.Get(ctx, lKey).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil // Cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read latest version pointer %s: %w", lKey, err)
	}

	latestVersion, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid version pointer %q at key %s: %w", val, lKey, err)
	}

	if minVersion > 0 && latestVersion < minVersion {
		// Cache is stale relative to requested causal min_version; return cache miss for DB fallback
		return nil, nil
	}

	vKey := VersionedKey(tID, kID, latestVersion)
	payload, err := c.client.Get(ctx, vKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil // Payload missing or evicted
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read versioned payload %s: %w", vKey, err)
	}

	var kb entity.KnowledgeBase
	if err := json.Unmarshal(payload, &kb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached knowledge base from %s: %w", vKey, err)
	}

	return &kb, nil
}

// Ping checks cluster connectivity for readiness probes (/ready).
func (c *ClusterClient) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// Close closes the underlying Redis Cluster connections.
func (c *ClusterClient) Close() error {
	return c.client.Close()
}
