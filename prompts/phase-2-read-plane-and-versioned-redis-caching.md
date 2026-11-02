# Implementation Prompt: Phase 2 — Read Plane & Versioned Redis Caching

## 1. Objective & Scope

Implement **Phase 2 (Weeks 3–4): Read Plane & Versioned Redis Caching** for `rag-kb-catalog-service` (Go 1.23+), strictly adhering to [`GEMINI.MD`](../GEMINI.MD), [`.ai.md`](../.ai.md), the [`.ai/`](../.ai/) specification suite, and `tasks/RAG KB Catalog Service Implementation Plan.pdf`.

### Core Architectural Invariants for Phase 2:
1. **Direct Redis 7.2 Cluster Versioned Cache (`ADR-003`)**:
   - Monotonic versioned payload key: `kb:{tenant_id}:{id}:v{version}`
   - Latest version pointer key: `kb:{tenant_id}:{id}:latest_version`
   - CRC16 Hash Tag Co-Location: `{tenant_id}` is hash-tagged to guarantee both keys land in the exact same Redis Cluster hash slot, eliminating `CROSSSLOT` errors on multi-key pipelined/Lua executions.
   - Bounded Safety TTL: Default 1 hour (`3600s`).
   - Zero in-process L1 caching and zero Pub/Sub invalidation buses.
2. **Causal Freshness & Bounded Eventual Consistency (`min_version`)**:
   - `GetKnowledgeBase` accepts optional `min_version uint64`. If `cached_version < min_version` or cache miss occurs, the read plane falls back directly to PostgreSQL (`pgxpool` with `default_query_exec_mode = exec`), retrieves the authoritative state, backfills `kb:{tenant_id}:{id}:v{version}`, and advances `latest_version` via monotonic Lua script (`if new_ver > cur_ver then redis.call('SET', ...)`).
   - Tombstone caching: Soft-deleted entities (`state = 'TOMBSTONED'`) are cached at the incremented version so subsequent reads return `ErrKnowledgeBaseNotFound` without triggering unnecessary database lookups.
3. **Hierarchical Taxonomy Engine**:
   - Validates and parses standardized hierarchical key-value namespaces (`namespace:key=value` or `namespace:value`, e.g. `env:production`, `compliance:hipaa`).
   - Supports query federation and filtering across multi-entity queries (`ListKnowledgeBases`).
4. **Readiness Probe (`GET /ready`) Update**:
   - Evaluates both PostgreSQL pool connectivity and Redis 7.2 Cluster ping. Returns `{"status": "ready", "postgres": "connected", "redis_cluster": "connected"}` when healthy, or HTTP `503 Service Unavailable` if either dependency fails.

---

## 2. Component Specifications & Files to Create / Modify

### A. Redis Cluster Infrastructure (`internal/infrastructure/cache/redis/cluster_client.go`)
- Create `ClusterClient` wrapping `redis.ClusterClient` (from `github.com/redis/go-redis/v9`).
- Implement `repository.VersionedCache` port:
  - `GetLatest(ctx context.Context, tenantID, kbID string, minVersion uint64) (*entity.KnowledgeBase, error)`
  - `PutVersion(ctx context.Context, kb *entity.KnowledgeBase) error`
- Key generation helpers with CRC16 `{tenant_id}` tags:
  - `versionedKey(tenantID, kbID string, version uint64) string` -> `kb:{tenant_id}:{kb_id}:v{version}`
  - `latestKey(tenantID, kbID string) string` -> `kb:{tenant_id}:{kb_id}:latest_version`
- Monotonic pointer update using Lua script:
  ```lua
  local cur = redis.call('GET', KEYS[1])
  if not cur or tonumber(ARGV[1]) > tonumber(cur) then
    redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
    return 1
  end
  return 0
  ```
- Graceful connection pooling, bounded timeouts (context-aware), and ping check for `/ready`.

### B. Application Layer Integration (`internal/application/`)
- Ensure cache publication in commands (`CreateKnowledgeBaseCommand`, `UpdateKnowledgeBaseCommand`, `TriggerReindexingCommand`, `PromotePointerCommand`, `AbortReindexingCommand`, `DeleteKnowledgeBaseCommand`) guarantees that `PutVersion` failures after successful database commit are not silently discarded: ensure failed publication is repaired durably or causes `GetKnowledgeBaseQuery` to read from the database until the cache is current, preventing stale cached metadata from being served.
- Ensure `GetKnowledgeBaseQuery` and `ListKnowledgeBasesQuery` operate cleanly with the versioned cache.

### C. Server Wiring & Environment (`cmd/catalog-service/main.go`)
- Read `REDIS_CLUSTER_ADDRS` (comma-separated list of seed nodes, e.g. `localhost:7000,localhost:7001,localhost:7002`), `REDIS_PASSWORD`.
- If `REDIS_CLUSTER_ADDRS` is provided, initialize `ClusterClient` and wire into commands and queries; update `readinessCheck` to verify Redis Cluster ping.
- If unconfigured, support deterministic in-memory versioned cache for standalone / unit tests.

### D. Unit & Integration Test Suites (`test/unit/`, `test/integration/`)
- `test/unit/redis_cache_test.go`: Test versioned key generation, CRC16 slot hashing invariants, monotonic pointer Lua updates, `min_version` cache-aside fallback, and tombstone caching.
- `test/unit/taxonomy_federation_test.go`: Comprehensive unit tests for taxonomy filtering and multi-namespace queries.
- Integration tests validating zero cache drift across concurrent updates.

---

## 3. Verification Checklist

1. `gofmt -s -w .`, `go vet ./...`, `golangci-lint run ./...`
2. Domain layer purity: zero third-party I/O imports in `internal/domain/`.
3. Secured logging: all Redis connection logs redact passwords/credentials.
4. Continuous Doc API & OpenAPI 3.1 synchronization (`.ai/doc-api.md`, `api/openapi/openapi.yaml`).
5. All test suites pass with `-race`.

