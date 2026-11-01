# Code Standards - RAG Knowledge Base Catalog Service

## 1. General Engineering Principles

- **Pragmatic Hexagonal Architecture**: Isolate pure domain entities, state machines, and policies (`internal/domain/`) from application use-case orchestration (`internal/application/`), concrete infrastructure adapters (`internal/infrastructure/`), and inbound HTTP/gRPC transport layers (`internal/transport/`).
- **Zero Third-Party I/O in Domain Layer**: Packages under `internal/domain/` must never import `pgx`, `go-redis`, `temporal`, `grpc`, or `net/http`.
- **Fail-Fast Validation**: Validate tenant context, RBAC roles, static model registry constraints, and hierarchical taxonomy syntax (`namespace:key=value`) before initiating database transactions or network calls.
- **Root-Cause Concurrency Control**: Never rely on sleep statements or volatile in-process locks to resolve concurrent updates; enforce Optimistic Concurrency Control (OCC) in PostgreSQL and monotonic versioned keys in Redis Cluster.

---

## 2. Go 1.23+ & Code Quality Standards

- **Runtime Target**: Go 1.23+ (`go 1.23` in `go.mod`).
- **Formatting & Linting**:
  - Format all code with `gofmt -s -w .`.
  - Pass `go vet ./...` and `golangci-lint run ./...` with zero warnings or errors.
- **Memory & Allocation Discipline**:
  - Maintain pod Resident Set Size (RSS) $\le 128\text{MiB}$ under 3,000 RPS peak concurrency.
  - Reuse serialization buffers via `sync.Pool` along hot read/write paths.
- **Context Propagation**:
  - Every repository, cache, workflow trigger, and transport handler function must accept `ctx context.Context` as its first parameter and respect context cancellation and timeouts.

---

## 3. Persistence, Caching & Concurrency Invariants

1. **PgBouncer Transaction Mode Compatibility (`default_query_exec_mode = exec`)**:
   - PostgreSQL connections via `github.com/jackc/pgx/v5/pgxpool` MUST explicitly set `config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec` (or `default_query_exec_mode=exec` in DSN) to prevent prepared statement collisions under PgBouncer transaction pooling.
2. **Strict Tenant Isolation**:
   - Every table in PostgreSQL (`knowledge_bases`, `kb_migrations`, `outbox_events`) enforces compound keys or explicit indices scoped by `tenant_id`.
   - Repository methods MUST reject any query or mutation lacking a non-empty, validated `tenant_id`.
3. **Optimistic Concurrency Control (OCC - ADR-004)**:
   - All entity mutations execute conditionally against the monotonic `version` column (`UPDATE ... SET version = version + 1 WHERE tenant_id = $1 AND kb_id = $2 AND version = $3`).
   - If `RowsAffected() == 0`, return domain error `ErrVersionConflict`, mapped deterministically to HTTP `409 Conflict` and gRPC `codes.Aborted`.
4. **Transactional Outbox Atomicity (ADR-005)**:
   - State mutations (`KBCreated`, `ReindexInitiated`, `PointerPromoted`, `KBDeleted`) and their corresponding `outbox_events` insert MUST execute inside the same `pgx.Tx` database transaction.
   - Do not implement internal polling loops or `LISTEN/NOTIFY` workers; Debezium tails the PostgreSQL WAL directly.
5. **Versioned Redis Cluster Keys (ADR-003)**:
   - Cache active Knowledge Base configurations in Redis 7.2 Cluster using deterministic versioned keys: `kb:{tenant_id}:{id}:v{version}` with a bounded safety TTL (e.g., 1 hour).
   - Do not introduce in-process L1 memory caches or Redis Pub/Sub invalidation buses.

---

## 4. Secured Logging Invariants

- **Structured JSON Logging**: All application logs MUST be emitted as machine-parseable JSON using Go's standard `log/slog` (`slog.NewJSONHandler`) with zero raw `fmt.Print*` or unstructured `log.Print*` calls in production packages.
- **Mandatory Correlation Fields**: Include `request_id`, `tenant_id`, `kb_id`, `version`, `method`, and `duration_ms` on operational log entries.
- **Strict Redaction Policy**:
  - **NEVER** log raw JWT tokens, `Authorization` headers, API keys, PostgreSQL DSNs/passwords, Redis credentials, or sensitive tenant PII/PHI payloads.
  - Implement a custom `slog.LogValuer` or `ReplaceAttr` redaction filter that masks sensitive keys (`password`, `secret`, `token`, `authorization`, `dsn`) with `"[REDACTED]"` before log emission.
  - Sanitize internal stack traces and SQL driver details before returning error responses to external clients.

---

## 5. Continuous Doc API & OpenAPI Maintenance

- **Mandatory Synchronization**: Whenever any HTTP route, gRPC RPC, Protobuf message, domain error mapping, or persistence schema is created or modified—no matter how small—both [`.ai/doc-api.md`](.ai/doc-api.md) and `api/openapi/openapi.yaml` (served at `/docs`, `/redoc`, `/api/docs`, `/openapi.json`) **MUST** be updated in the same working step.

---

## 6. Git Commit Standards

- **Git Commit Frequency**: Commit code as you build. Every working session or phase must contain **at least two git commits**.
- **Conventional Commit Patterns**: All git commit messages must strictly adhere to the following prefixes:
  - `feat:` for new features 
  - `fix:` for bug fixes
  - `refactor:` for code refactoring
  - `docs:` for documentation updates
  - `tests:` for tests addition and modification
  - `chore:` for maintenance tasks and environment setup
