# Implementation Prompt: Phase 1 — Core Schemas & Concurrency Controls

## 1. Objective & Scope

Implement **Phase 1 (Weeks 1–2): Core Schemas & Concurrency Controls** for `rag-kb-catalog-service` (Go 1.23+), strictly adhering to [`GEMINI.MD`](../GEMINI.MD), [`.ai.md`](../.ai.md), the [`.ai/`](../.ai/) specification suite, and the local architectural blueprints in `tasks/` (`RAG KB Catalog Service Implementation Plan.pdf`, `Production Grade Backend Concepts.pdf`, `Production-Grade Multimodal RAG Architectural Specification.pdf`, `Disaggregated Hybrid Model Architecture.pdf`, `Go and Python Microservices Architecture.pdf`, and `My Naming Convention.pdf`).

### Phase Boundary & Cross-Phase Port Strategy (`F-01`)
To satisfy the full OpenAPI 3.1 (`api/openapi/openapi.yaml`) and Protobuf v3 (`api/proto/v1/catalog.proto`) contracts required by CI (`.github/workflows/ci.yml`) while respecting the 8-week roadmap in `RAG KB Catalog Service Implementation Plan.pdf`:
- **Full Phase 1 Production Implementation**: PostgreSQL 16 DDL & migrations, PgBouncer `default_query_exec_mode = exec` connection pooling, Optimistic Concurrency Control (`version = version + 1`), transactional outbox writes (`outbox_events`), idempotency key deduplication (`idempotency_keys`), pure hexagonal domain layer (aggregate root, vector schema immutability guard, static model registry, Blue-Green migration state machine, hierarchical taxonomy parser, coarse RBAC evaluator), application commands/queries, gRPC server + interceptors, and OpenAPI 3.1 HTTP server + middlewares + operational probes.
- **Clean Hexagonal Ports for Phase 2 (Redis Cluster) & Phase 3 (Temporal)**:
  - `get_knowledge_base.go` accepts an optional `VersionedCache` port interface; when nil or unconfigured in Phase 1 (prior to Phase 2's `internal/infrastructure/cache/redis/cluster_client.go`), reads resolve directly against PostgreSQL with full `min_version` validation.
  - `GET /ready` evaluates active PostgreSQL pool connectivity (`pool.Ping(ctx)`) and an injectable `RedisHealthChecker` function (returning `{"status": "ready", "postgres": "connected", "redis_cluster": "connected"}` when Redis is healthy, `{"status": "ready", "postgres": "connected", "redis_cluster": "disabled"}` or omitted when Redis is in Phase 1 bypass/noop mode, and HTTP `503 Service Unavailable` when PostgreSQL is unreachable).
  - `internal/application/workflow/ingestion_trigger.go` defines the `IngestionTrigger` port interface alongside a deterministic no-op/in-memory Phase 1 adapter wired in `cmd/catalog-service/main.go` so `TriggerReindexing` executes safely without nil-pointer panics prior to Phase 3's Temporal SDK adapter (`internal/infrastructure/temporal/client.go`).

---

## 2. Detailed Layer Specifications

### A. PostgreSQL 16 DDL & Migrations (`internal/infrastructure/persistence/migrations/`)
Create deterministic SQL migrations (`000001_init_catalog_schema.up.sql` and `000001_init_catalog_schema.down.sql`) defining:
1. **`knowledge_bases`**:
   - Compound primary key `(tenant_id, kb_id)`, unique constraint `uq_kb_tenant_name UNIQUE (tenant_id, name)`.
   - Monotonic `version BIGINT NOT NULL DEFAULT 1`.
   - `state TEXT NOT NULL CHECK (state IN ('CREATING', 'ACTIVE', 'REINDEXING', 'PROMOTED', 'ABORTED', 'TOMBSTONED'))`.
   - `routing_policy TEXT NOT NULL DEFAULT 'GREEN_ONLY' CHECK (routing_policy IN ('GREEN_ONLY', 'DUAL_WRITE'))`.
   - `active_index_pointer TEXT NOT NULL`, `candidate_index_pointer TEXT NOT NULL DEFAULT ''`.
   - `active_schema JSONB NOT NULL`, `candidate_schema JSONB`, `retrieval_profile JSONB NOT NULL`, `taxonomy_tags JSONB NOT NULL DEFAULT '[]'::jsonb`.
   - `created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`, `updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`.
   - Indices: GIN index `idx_kb_tenant_taxonomy ON knowledge_bases USING GIN (taxonomy_tags)` and composite index `idx_kb_tenant_state ON knowledge_bases (tenant_id, state, kb_id)`.
2. **`kb_migrations`**:
   - Compound primary key `(tenant_id, migration_id)` and foreign key `(tenant_id, kb_id)` referencing `knowledge_bases (tenant_id, kb_id) ON DELETE CASCADE`.
   - Columns: `source_green_pointer`, `target_blue_pointer`, `source_schema`, `target_schema`, `state CHECK (state IN ('REINDEXING', 'PROMOTED', 'ABORTED'))`, `delta_lag_records BIGINT NOT NULL DEFAULT 0`, `temporal_workflow_id TEXT NOT NULL DEFAULT ''`, `abort_reason TEXT NOT NULL DEFAULT ''`, `created_at`, `updated_at`.
   - Indices (`F-02`):
     - `idx_kb_migrations_active ON kb_migrations (tenant_id, kb_id, state)`
     - **Partial Unique Index**: `CREATE UNIQUE INDEX IF NOT EXISTS uq_kb_migrations_one_active ON kb_migrations (tenant_id, kb_id) WHERE state = 'REINDEXING';` to guarantee at the storage engine level that at most one active Blue-Green migration can exist per Knowledge Base.
3. **`outbox_events` (`ADR-005`)**:
   - Compound primary key `(tenant_id, event_id)` storing atomic CDC domain events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`), `aggregate_type`, `aggregate_id`, `aggregate_version`, `payload JSONB`, `created_at`, and index `idx_outbox_tenant_created ON outbox_events (tenant_id, created_at)`.
4. **`idempotency_keys` (`F-05`)**:
   - Compound primary key `(tenant_id, idempotency_key)` with `operation`, `request_hash`, `response_payload JSONB`, `status_code INT`, `created_at`, and `expires_at TIMESTAMPTZ`.

### B. Pure Hexagonal Domain Layer (`internal/domain/`) — Zero Third-Party I/O Imports (`ADR-006`)
1. **`internal/domain/entity/knowledge_base.go`**:
   - Aggregate root (`KnowledgeBase`), `LifecycleState`, `RoutingPolicy`, `DistanceMetric`, `VectorSchema`, `RetrievalProfile`, domain sentinel errors (`ErrKnowledgeBaseNotFound`, `ErrDuplicateKBName`, `ErrVersionConflict`, `ErrStructuralMutationDisallowed`, `ErrInvalidStateTransition`, `ErrDeltaLagNotZero`, `ErrInsufficientPermissions`, `ErrTenantMismatch`, `ErrIdempotencyKeyConflict`, `ErrInvalidInput`), and state transition methods.
   - **Creation Settlement (`F-11`)**: `NewKnowledgeBase(...)` initializes in `CREATING`, validates schema/profile/taxonomy invariants, assigns `active_index_pointer = idx_{tenant_id}_{kb_id}_green_v1`, and settles at `ACTIVE` (`version = 1`, `routing_policy = GREEN_ONLY`) prior to persistence.
2. **`internal/domain/entity/migration_state.go`**:
   - Blue-Green state machine (`ValidateTransition(from, to LifecycleState, authoritativeDeltaLag uint64)`), enforcing `ADR-001` and `ADR-002`:
     - `CREATING` -> `ACTIVE` (Allowed)
     - `ACTIVE` -> `REINDEXING` (Allowed; sets `routing_policy = DUAL_WRITE` and allocates `candidate_index_pointer = idx_{tenant_id}_{kb_id}_blue_v{version+1}`)
     - `REINDEXING` -> `PROMOTED` (Allowed **only if** `authoritativeDeltaLag == 0`; otherwise returns `ErrDeltaLagNotZero`; post-promotion aggregate operational state settles at `ACTIVE` with `active_index_pointer = candidate_index_pointer`, `active_schema = candidate_schema`, `candidate_index_pointer = ""`, `candidate_schema = nil`, `routing_policy = GREEN_ONLY`, and `kb_migrations.state = 'PROMOTED'`)
     - `REINDEXING` -> `ABORTED` (Allowed; post-abort aggregate operational state settles at `ACTIVE` with `active_index_pointer` locked to Green, `candidate_index_pointer = ""`, `candidate_schema = nil`, `routing_policy = GREEN_ONLY`, and `kb_migrations.state = 'ABORTED'`)
     - Direct `REINDEXING` -> `ACTIVE` without explicit promotion or abort is strictly rejected (`ErrInvalidStateTransition`).
3. **`internal/domain/entity/model_reference.go`**:
   - Static Validated Model Registry (`ADR-006`) validating supported `embedding_model` identifiers, exact `dimensions` constraints, allowed `distance_metric` (`COSINE`, `DOT_PRODUCT`, `L2`), and non-empty `schema_version`.
4. **`internal/domain/entity/taxonomy.go`**:
   - Hierarchical key-value namespace parser (`namespace:key=value` or `namespace:value`, e.g., `env:production`, `compliance:hipaa`, `domain:finance`), rejecting malformed tokens, whitespace/control characters, or empty segments, and deduplicating/sorting tags deterministically.
5. **`internal/domain/policy/immutability_guard.go` (`F-06`)**:
   - Enforces vector schema immutability (`ADR-001`). Rejects any attempt to mutate `embedding_model`, `dimensions`, `distance_metric`, or `schema_version` in-place via `UpdateKnowledgeBase` with `ErrStructuralMutationDisallowed`.
6. **`internal/domain/policy/access_evaluator.go` (`F-07`, `F-09`)**:
   - Enforces strict multi-tenant context (`tenant_id` non-empty, `user_id` non-empty) and the deterministic RBAC permission matrix:
     - **Read (`GetKnowledgeBase`, `ListKnowledgeBases`)**: `Owner`, `Editor`, `Viewer`, `Auditor`
     - **Mutate (`CreateKnowledgeBase`, `UpdateKnowledgeBase`, `TriggerReindexing`)**: `Owner`, `Editor`
     - **Cutover & Delete (`PromoteIndexPointer`, `AbortReindexing`, `DeleteKnowledgeBase`)**: `Owner`, `Editor` (`Viewer` and `Auditor` rejected with `ErrInsufficientPermissions`)
7. **`internal/domain/event/events.go`**:
   - Domain events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) and deterministic JSON payload builders for `outbox_events`.
8. **`internal/domain/repository/catalog_repository.go` & `outbox_repository.go`**:
   - Port interfaces for transactional persistence, OCC mutations, migration lag queries, and idempotency deduplication.

### C. Application Layer (`internal/application/`)
1. **Commands (`internal/application/command/`)**:
   - `create_knowledge_base.go`: Enforces RBAC, validates model/profile/taxonomy, reconciles `Idempotency-Key` header and `CreateKBRequest.idempotency_key` (`F-05`), checks SHA-256 `request_hash` against `idempotency_keys`, and atomically persists `knowledge_bases`, `outbox_events` (`KBCreated`), and `idempotency_keys` (24h TTL).
   - `update_knowledge_base.go`: Enforces RBAC, evaluates `immutability_guard.go` against `attempted_schema_mutation` (`F-06`), applies `update_mask` selected-field semantics (distinguishing omitted `taxonomy_tags` from explicit `taxonomy_tags: []` clear), and executes OCC update (`version = version + 1`) + `KBUpdated` outbox event.
   - `trigger_reindexing.go`: Validates `target_blue_schema` against the static model registry, transitions `ACTIVE` -> `REINDEXING` (`routing_policy = DUAL_WRITE`), atomically updates `knowledge_bases`, inserts `kb_migrations` (`state = 'REINDEXING'`), writes `ReindexInitiated` to `outbox_events`, and invokes `IngestionTrigger`.
   - `promote_pointer.go` (`F-03`): Reads the server's authoritative `kb_migrations.delta_lag_records` inside the transaction (never trusting client-supplied `verified_delta_lag_records` alone; if client passes a non-nil `verified_delta_lag_records != 0` or server authoritative `delta_lag_records > 0`, returns `ErrDeltaLagNotZero`), transitions `REINDEXING` -> `PROMOTED` (settling KB operational state at `ACTIVE` with Blue pointer promoted), updates `kb_migrations.state = 'PROMOTED'`, and emits `PointerPromoted` to `outbox_events`.
   - `abort_reindexing.go` (`F-08`): Returns `(*dto.KBResponse, error)` so both REST (`200 OK` with `KBResponse`) and gRPC (`google.protobuf.Empty`) are served cleanly from the same command. Transitions `REINDEXING` -> `ABORTED` (settling KB operational state at `ACTIVE` with Green pointer locked), records `abort_reason` in `kb_migrations`, and emits `ReindexAborted` (Blue cleanup event) to `outbox_events`.
   - `delete_knowledge_base.go`: Soft-deletes KB (`state = 'TOMBSTONED'`, `version = version + 1`) under OCC and emits `KBDeleted` purge event to `outbox_events`.
2. **Queries (`internal/application/query/`)**:
   - `get_knowledge_base.go`: Enforces RBAC and tenant scoping; supports optional `VersionedCache` port with `min_version` causal freshness check and PostgreSQL fallback (`F-01`).
   - `list_knowledge_bases.go`: Enforces RBAC, validates `taxonomy_filter` tags via `taxonomy.go`, enforces pagination bounds (`1 <= page_size <= 100`, default `20`), and queries PostgreSQL using the GIN `@>` containment operator and keyset cursor pagination (`page_token`).
3. **DTOs & Workflow Trigger Port (`internal/application/dto/catalog_dto.go`, `internal/application/workflow/ingestion_trigger.go`)**.

### D. PostgreSQL Persistence & Observability Infrastructure (`internal/infrastructure/`)
1. **`internal/infrastructure/persistence/postgres/pool.go`**:
   - `pgxpool` connection pool enforcing `config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec` (`default_query_exec_mode = exec`) for PgBouncer transaction-pooling compatibility (`ADR-004`).
2. **`internal/infrastructure/persistence/postgres/catalog_repo.go` (`F-04`)**:
   - Enforces `tenant_id` isolation on every SQL statement.
   - **Strict OCC Disambiguation (`F-04`)**: Inside every mutation transaction (`pgx.Tx`), inspects the current row under lock (`SELECT ... FROM knowledge_bases WHERE tenant_id = $1 AND kb_id = $2 FOR NO KEY UPDATE` or re-reading under `FOR NO KEY UPDATE` after a conditional update affects zero rows) so that:
     - Missing or `TOMBSTONED` KB returns `ErrKnowledgeBaseNotFound` (`404 Not Found` / gRPC `NotFound`).
     - Invalid lifecycle state returns `ErrInvalidStateTransition` (`409 Conflict` / gRPC `FailedPrecondition`).
     - Version mismatch (`actual_version != expected_version` or `RowsAffected() == 0` on concurrent modification) returns `ErrVersionConflict` (`409 Conflict` / gRPC `Aborted`) and **exclusively** increments `kb_catalog_optimistic_conflicts_total`.
   - Commits entity state updates, `kb_migrations` updates, `idempotency_keys` inserts, and `outbox_events` inserts atomically in the same `pgx.Tx`.
3. **`internal/infrastructure/persistence/postgres/outbox_repo.go`**:
   - Transactional outbox insertion helper operating on `pgx.Tx`.
4. **`internal/infrastructure/observability/` (`F-11`)**:
   - `logger.go`: Structured `log/slog` JSON logger (`slog.NewJSONHandler`) with custom `ReplaceAttr` redaction masking `Authorization`, `Bearer`, `token`, `jwt_token`, `raw_token`, `password`, `secret`, `dsn`, and `api_key` values as `"[REDACTED]"`.
   - `metrics.go`: Prometheus metrics matching `.ai/doc-api.md` Section 5:
     - `kb_catalog_requests_total` (CounterVec: `["method", "tenant", "status_code"]`)
     - `kb_catalog_request_duration_seconds` (HistogramVec: `["method", "protocol"]`)
     - `kb_catalog_optimistic_conflicts_total` (CounterVec: `["tenant", "operation"]`)
     - `kb_catalog_migration_delta_lag_records` (GaugeVec: `["tenant", "kb_id"]`)
     - `kb_catalog_active_migrations` (GaugeVec: `["tenant"]`)
   - `buffer_pool.go`: `sync.Pool` recycling `*bytes.Buffer` instances (with capacity cap guard to prevent pinning oversized buffers) to maintain pod RSS $\le 128\text{MiB}$.

### E. gRPC & OpenAPI 3.1 HTTP Transport Layers (`api/proto/v1/`, `internal/transport/`, `cmd/catalog-service/main.go`)
1. **Protobuf Contracts & Pre-Compiled Go Bindings (`F-10`)**:
   - `api/proto/v1/catalog.proto` & `api/proto/v1/types.proto`
   - Committed Go bindings (`api/proto/v1/types.pb.go`, `api/proto/v1/catalog.pb.go`, `api/proto/v1/catalog_grpc.pb.go`) so `go vet`, `golangci-lint`, and `go test -race` pass in CI without requiring external `protoc` installation.
2. **HTTP REST & Doc API Transport (`internal/transport/http/`)**:
   - `router.go` & `handler.go`:
     - Administrative routes: `POST /v1/knowledge-bases`, `GET /v1/knowledge-bases`, `GET /v1/knowledge-bases/{kb_id}`, `PATCH /v1/knowledge-bases/{kb_id}` (detecting in-place schema mutations across `attempted_schema_mutation`, `active_schema`, or `vector_schema` JSON fields per `F-06`), `DELETE /v1/knowledge-bases/{kb_id}`, `POST /v1/knowledge-bases/{kb_id}:triggerReindexing`, `POST /v1/knowledge-bases/{kb_id}:promotePointer`, `POST /v1/knowledge-bases/{kb_id}:abortReindexing` (returning `200 OK` with `KBResponse` per `F-08`).
     - OpenAPI 3.1 & Operational Probes: `GET /docs` (Swagger UI), `GET /redoc` (ReDoc UI), `GET /api/docs` & `GET /openapi.json` (serving OpenAPI 3.1 spec), `GET /healthz`, `GET /ready`, and `GET /metrics`.
   - `middleware/`: Request ID correlation (`X-Request-ID`), Bearer JWT verification & claim extraction (`tenant_id`, incoming `sub`/`subject` explicitly mapped to canonical `user_id` in request context, and `roles`), secured `log/slog` JSON request logging, panic recovery, and Prometheus instrumentation.
3. **gRPC Transport (`internal/transport/grpc/`) (`F-09`)**:
   - `catalog_server.go` & `interceptors/auth_logging.go`:
     - Extracts verified caller identity from gRPC metadata/JWT context (`tenant_id`, `sub` -> `user_id`, `roles`).
     - **Tenant Spoofing Defense (`F-09`)**: If a gRPC request payload contains a non-empty `tenant_id` that differs from the verified context `tenant_id`, rejects immediately with `codes.PermissionDenied` (`ErrTenantMismatch`).
     - Deterministic domain-to-gRPC status mapping:
       - `ErrStructuralMutationDisallowed` -> `codes.FailedPrecondition` (HTTP `422 Unprocessable Entity`)
       - `ErrVersionConflict` -> `codes.Aborted` (HTTP `409 Conflict`)
       - `ErrDeltaLagNotZero` -> `codes.FailedPrecondition` (HTTP `409 Conflict`)
       - `ErrInvalidStateTransition` -> `codes.FailedPrecondition` (HTTP `409 Conflict`)
       - `ErrDuplicateKBName` / `ErrIdempotencyKeyConflict` -> `codes.AlreadyExists` (HTTP `409 Conflict`)
       - `ErrKnowledgeBaseNotFound` -> `codes.NotFound` (HTTP `404 Not Found`)
       - `ErrInsufficientPermissions` / `ErrTenantMismatch` -> `codes.PermissionDenied` (HTTP `403 Forbidden`)
       - `ErrInvalidInput` -> `codes.InvalidArgument` (HTTP `400 Bad Request`)

---

## 3. Files to Create / Modify

- `go.mod`, `go.sum`, `Makefile`
- `api/proto/v1/catalog.proto`, `api/proto/v1/types.proto`
- `api/proto/v1/types.pb.go`, `api/proto/v1/catalog.pb.go`, `api/proto/v1/catalog_grpc.pb.go`
- `api/openapi/openapi.yaml` & `.ai/doc-api.md` (continuous synchronization required by CI)
- `internal/infrastructure/persistence/migrations/000001_init_catalog_schema.up.sql`
- `internal/infrastructure/persistence/migrations/000001_init_catalog_schema.down.sql`
- `internal/domain/entity/knowledge_base.go`
- `internal/domain/entity/migration_state.go`
- `internal/domain/entity/model_reference.go`
- `internal/domain/entity/taxonomy.go`
- `internal/domain/policy/immutability_guard.go`
- `internal/domain/policy/access_evaluator.go`
- `internal/domain/event/events.go`
- `internal/domain/repository/catalog_repository.go`
- `internal/domain/repository/outbox_repository.go`
- `internal/application/dto/catalog_dto.go`
- `internal/application/workflow/ingestion_trigger.go`
- `internal/application/command/create_knowledge_base.go`
- `internal/application/command/update_knowledge_base.go`
- `internal/application/command/trigger_reindexing.go`
- `internal/application/command/promote_pointer.go`
- `internal/application/command/abort_reindexing.go`
- `internal/application/command/delete_knowledge_base.go`
- `internal/application/query/get_knowledge_base.go`
- `internal/application/query/list_knowledge_bases.go`
- `internal/infrastructure/observability/logger.go`
- `internal/infrastructure/observability/metrics.go`
- `internal/infrastructure/observability/buffer_pool.go`
- `internal/infrastructure/persistence/postgres/pool.go`
- `internal/infrastructure/persistence/postgres/catalog_repo.go`
- `internal/infrastructure/persistence/postgres/outbox_repo.go`
- `internal/transport/http/router.go`
- `internal/transport/http/handler.go`
- `internal/transport/http/middleware/auth.go`
- `internal/transport/http/middleware/logging.go`
- `internal/transport/grpc/catalog_server.go`
- `internal/transport/grpc/interceptors/auth_logging.go`
- `cmd/catalog-service/main.go`
- `test/unit/immutability_test.go`
- `test/unit/migration_state_test.go`
- `test/unit/taxonomy_test.go`
- `test/unit/access_evaluator_test.go`
- `test/unit/http_grpc_transport_test.go`
- `.ai/progress-tracker.md`

---

## 4. Security & Non-Negotiable Invariant Safeguards

1. **Hexagonal Domain Purity (`ADR-006`)**: `internal/domain/...` imports only Go standard library packages; zero `pgx`, `go-redis`, `temporal`, `grpc`, or `net/http` imports.
2. **Secured Logging**: All logs emitted via `slog.NewJSONHandler` with custom `ReplaceAttr` redaction masking `Authorization`, `Bearer`, `token`, `password`, `secret`, `dsn` as `"[REDACTED]"`. Zero `fmt.Print*` or `log.Print*` in production packages.
3. **Strict Tenant Isolation & Spoofing Defense**: `tenant_id` extracted exclusively from verified JWT/context claims (`sub` mapped to `user_id`), enforced on every repository call, and checked against gRPC request `tenant_id` fields (`ErrTenantMismatch`).
4. **PgBouncer Compatibility & OCC Disambiguation (`ADR-004`)**: `pgx.QueryExecModeExec` enforced on pool creation; every mutation checks `WHERE tenant_id = $1 AND kb_id = $2 AND version = $3` and disambiguates `ErrKnowledgeBaseNotFound` (`404`) and `ErrInvalidStateTransition` (`409` / `FailedPrecondition`) from `ErrVersionConflict` (`409` / `Aborted`).
5. **Continuous Doc API & OpenAPI 3.1 Sync**: Keep `.ai/doc-api.md` and `api/openapi/openapi.yaml` synchronized with all endpoints, schemas, and error codes in the same step so `.github/workflows/ci.yml` (`openapi-and-doc-api-sync`) passes.
6. **Git Commit Standards (`F-12`)**: Create **at least two conventional git commits** (`feat:`, `tests:`, `docs:`, `chore:`) during Phase 1 execution.

---

## 5. Verification Steps

1. Format Go code: `gofmt -s -w .`
2. Run static analysis: `go vet ./...`
3. Run linter: `golangci-lint run ./...` (if installed)
4. Verify zero forbidden imports in `internal/domain/` (`grep -rnE 'github\.com/jackc/pgx|github\.com/redis/go-redis|go\.temporal\.io|google\.golang\.org/grpc|net/http' internal/domain/`)
5. Audit secured logging (`log/slog` JSON, redaction of credentials/tokens, zero raw `fmt.Print*`/`log.Print*`) and PgBouncer `QueryExecModeExec`
6. Validate `api/openapi/openapi.yaml` and `.ai/doc-api.md` synchronization
7. Run unit, integration, and chaos test suites with race detection: `go test -v -race ./internal/... ./test/unit/... ./test/integration/... ./test/chaos/...` (or `./...`)
8. Update `.ai/progress-tracker.md` and verify at least two conventional git commits
