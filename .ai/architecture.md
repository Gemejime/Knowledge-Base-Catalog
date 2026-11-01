# Architecture Context - RAG Knowledge Base Catalog Service

## 1. System Stack & Specifications

| Layer | Technology | Role & Operational Characteristics |
| :--- | :--- | :--- |
| **Language Runtime** | Go 1.23+ | Idiomatic concurrency, strong static typing, and `sync.Pool` allocations for high-throughput serialization buffers (RSS $\le 128\text{MiB}$). |
| **Relational Storage** | PostgreSQL 16+ & PgBouncer | Transaction-mode connection pooling via `pgx/v5` configured with `default_query_exec_mode = exec` (`pgx.QueryExecModeExec`) to eliminate prepared statement incompatibilities. |
| **Caching Layer** | Redis 7.2 Cluster | Direct key-value metadata lookups using deterministic, versioned keys (`kb:{tenant_id}:{id}:v{version}`) with bounded safety TTL (1 hour), bypassing volatile in-process L1 invalidation buses. |
| **Workflow Integration** | Temporal Go SDK (`go.temporal.io/sdk`) | Restricted strictly to client-triggers scheduling backfill and compaction DAGs in `rag-ingestion-coordinator`. |
| **Event Streaming & CDC** | PostgreSQL WAL + Debezium + Kafka | Atomic transactional outbox pattern. Service commits events to the `outbox_events` table in the same SQL transaction as state mutations; zero internal polling workers. |
| **RPC & Network** | gRPC HTTP/2 (Protobuf v3) + OpenAPI 3.1 REST | Strongly-typed gRPC (`api/proto/v1/catalog.proto`) for internal control-plane mesh (`rag-search-orchestrator`, `rag-ingestion-coordinator`) and OpenAPI 3.1 HTTP REST gateway for platform administration. |
| **Container & K8s Runtime** | Distroless (`gcr.io/distroless/static:nonroot`) | Multi-stage non-root container image; CPU: `100m` req / `500m` lim; Memory: `64MiB` req / `128MiB` lim; `PodDisruptionBudget` (`minAvailable: 2`); cross-AZ anti-affinity. |

---

## 2. Production-Grade 5-Layer Backend Architecture

Aligned with the committed specifications in [`../.ai/`](../.ai/) (and the local-only untracked reference PDFs `tasks/Production Grade Backend Concepts.pdf` and `tasks/RAG KB Catalog Service Implementation Plan.pdf` when provisioned locally):

### Layer 1: API & Routing
1. **Entry Points (`cmd/catalog-service/main.go`)**:
   - Initializes runtime configuration, configures structured `log/slog` JSON logging with redaction, establishes the PgBouncer-compatible `pgxpool` PostgreSQL connection pool (`QueryExecModeExec`), connects to Redis 7.2 Cluster and Temporal Client, wires hexagonal dependencies, and orchestrates graceful shutdown on `SIGINT`/`SIGTERM` by draining active HTTP/gRPC connections.
2. **Endpoints & Routing (`internal/transport/http/router.go`, `internal/transport/grpc/catalog_server.go`)**:
   - **gRPC Service (`rag.catalog.v1.CatalogService`)**: `CreateKnowledgeBase`, `UpdateKnowledgeBase`, `GetKnowledgeBase`, `ListKnowledgeBases`, `DeleteKnowledgeBase`, `TriggerReindexing`, `PromoteIndexPointer`, `AbortReindexing`.
   - **REST Administrative Endpoints (`/v1/knowledge-bases`)**: Full CRUD, taxonomy filtering, and Blue-Green migration lifecycle triggers.
   - **Doc API & Operational Probes**:
     - `GET /docs`, `GET /redoc`: Interactive Swagger UI and ReDoc documentation.
     - `GET /api/docs`, `GET /openapi.json`: Machine-readable OpenAPI 3.1 specification synced with `api/openapi/openapi.yaml`.
     - `GET /healthz`: Liveness probe.
     - `GET /ready`: Readiness probe verifying active PostgreSQL pool and Redis Cluster connectivity.
     - `GET /metrics`: Prometheus scrape endpoint.
3. **Middleware & Interceptors (`internal/transport/http/middleware/`, `internal/transport/grpc/interceptors/`)**:
   - Request correlation ID injection (`X-Request-ID` / ULID/UUID), JWT claim extraction (`tenant_id`, incoming `sub`/`subject` explicitly mapped to canonical `user_id` in the request context before `access_evaluator.go` executes, and `roles`), structured request/response logging with PII/token redaction, panic recovery, and latency histogram recording.
4. **Input Validation & Sanitization**:
   - Strict schema validation at the transport boundary and domain invariant enforcement (`internal/domain/policy/immutability_guard.go`, `internal/domain/entity/model_reference.go`, `internal/domain/entity/taxonomy.go`).
   - Rejects malformed hierarchical namespace tags (`namespace:key=value`) and unsupported embedding model/dimension/metric combinations before hitting persistence.
5. **Rate Limiting & Throttling**:
   - Tenant-partitioned request rate governance and operational throttling hooks (Runbook 1: throttling batch ingestion traffic via API Gateway rate limiter when delta replay lag stalls).
6. **Idempotency & Optimistic Concurrency Control (OCC)**:
   - Support for `Idempotency-Key` headers on creation/migration commands.
   - All updates execute conditionally against a monotonic integer `version` attribute (`WHERE tenant_id = $1 AND id = $2 AND version = $3`). Conflicting concurrent updates deterministically return HTTP `409 Conflict` / gRPC `codes.Aborted` (`kb_catalog_optimistic_conflicts_total`).

### Layer 2: Data Persistence & State
7. **Database Migrations (`internal/infrastructure/persistence/migrations/`)**:
   - Version-controlled, deterministic SQL migrations defining `knowledge_bases`, `kb_migrations`, `outbox_events`, and `idempotency_keys` with compound tenant keys and indices.
8. **Database Transactions (ACID) & Transactional Outbox (`internal/infrastructure/persistence/postgres/`)**:
   - Every state mutation (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) executes inside a single atomic PostgreSQL ACID transaction that updates the entity row (`version = version + 1`), updates `kb_migrations` when applicable, AND inserts the domain event payload into the `outbox_events` table. If any operation fails, the entire transaction rolls back—guaranteeing zero lost CDC events for Debezium WAL tailing.
9. **Connection Pooling (`internal/infrastructure/persistence/postgres/pool.go`)**:
   - Managed `pgxpool.Pool` connecting through PgBouncer in transaction mode. Explicitly sets `default_query_exec_mode = exec` (`pgx.QueryExecModeExec`) to avoid prepared statement collisions across pooled backend connections.
10. **Caching (`internal/infrastructure/cache/redis/cluster_client.go`)**:
    - Direct Redis 7.2 Cluster read plane ($P99 \le 5\text{ms}$ at 3,000 RPS).
    - Uses monotonic versioned keys (`kb:{tenant_id}:{id}:v{version}`) co-located on the same Redis Cluster CRC16 hash slot via `{tenant_id}` hash tags alongside a monotonic version pointer (`kb:{tenant_id}:{id}:latest_version`) with a bounded safety TTL (1 hour / `3600s`), eliminating in-process L1 caches and Pub/Sub invalidation race conditions (ADR-003).
11. **Pagination & Query Limits (`internal/application/query/list_knowledge_bases.go`)**:
    - Cursor-based pagination (`page_token`, `page_size`) with strict upper bounds (e.g., max 100 items per page) and indexed taxonomy filtering to prevent OOM spikes.
12. **Resource Identification Strategies**:
    - High-entropy UUIDv4 / ULIDs for Knowledge Base IDs (`kb_id`), migration IDs, and outbox event IDs to prevent enumeration attacks, paired with human-readable tenant-scoped slugs/names.

### Layer 3: Architecture & Scale
13. **Asynchronous Task Queues, Workflows & CDC (`internal/infrastructure/temporal/client.go`)**:
    - Heavy backfill and compaction jobs are never executed in-process; the catalog service commits the `REINDEXING` and `DUAL_WRITE` state and transactional outbox event to PostgreSQL before starting `TriggerBackfillDAG`. The workflow is triggered through a durable post-commit mechanism with retry and idempotency, and cache publication ensures it cannot read stale `GREEN_ONLY` routing. Outbox events are streamed asynchronously by Debezium to Apache Kafka.
14. **High Availability & Redundancy (`deployments/helm/`)**:
    - Stateless Go replicas deployed with Kubernetes `PodDisruptionBudget` (`minAvailable: 2`), multi-AZ pod anti-affinity rules, and zero-downtime rolling updates.
15. **Creational Design Patterns & Memory Pooling**:
    - Singletons for `pgxpool.Pool`, Redis Cluster client, Temporal client, and Prometheus registry initialized once in `cmd/catalog-service/main.go`.
    - `sync.Pool` buffer recycling for JSON/Protobuf serialization to maintain a flat RSS footprint $\le 128\text{MiB}$.

### Layer 4: Security & Identity
16. **Authentication vs. Authorization (AuthN vs. AuthZ)**:
    - **AuthN**: Edge Gateway validates JWTs and propagates verified caller identity (`tenant_id`, `user_id`, `roles`) via gRPC metadata and HTTP headers.
    - **AuthZ (`internal/domain/policy/access_evaluator.go`)**: Enforces coarse entity-level RBAC/ABAC (`Owner`, `Editor`, `Viewer`, `Auditor`) and strict multi-tenant isolation. Every repository method requires an explicit, validated `tenant_id` and rejects unscoped queries.
17. **Configuration & Secrets Management**:
    - 12-Factor App configuration loaded from environment variables; database credentials, Redis passwords, and JWT verification keys are injected via secret stores and never committed to Git.
18. **Data Protection & Non-Root Hardening**:
    - TLS/mTLS in transit for gRPC, PostgreSQL, and Redis connections.
    - Multi-stage distroless container (`gcr.io/distroless/static:nonroot`) with all shells and non-essential binaries removed.

### Layer 5: Observability & Quality
19. **Observability (Secured Structured Logging, Metrics & Tracing)**:
    - **Secured Logging**: Machine-parseable JSON logs (`log/slog`) with mandatory correlation fields (`request_id`, `tenant_id`, `kb_id`, `version`, `method`, `duration_ms`) and strict redaction of JWTs, secrets, DSNs, and PII.
    - **Prometheus Metrics**:
      - `kb_catalog_requests_total` (Counter: `method`, `tenant`, `status_code`)
      - `kb_catalog_request_duration_seconds` (Histogram: P50, P90, P99)
      - `kb_catalog_optimistic_conflicts_total` (Counter: HTTP 409 / gRPC Aborted)
      - `kb_catalog_migration_delta_lag_records` (Gauge: outstanding delta write backlog before cutover; a Prometheus recording rule `record: kb_migration_delta_lag_records` -> `expr: kb_catalog_migration_delta_lag_records` is defined for Runbook 1 compatibility)
      - `kb_catalog_active_migrations` (Gauge: currently active Blue-Green migrations)
    - **Distributed Tracing**: OpenTelemetry trace context propagation across gRPC interceptors and HTTP middleware.
20. **Health Checks & Graceful Error Handling**:
    - Liveness (`/healthz`) and Readiness (`/ready`) probes monitoring active DB pool and Redis Cluster health.
    - Deterministic domain-to-transport error mapping (`ErrStructuralMutationDisallowed` -> HTTP `422 Unprocessable Entity` / gRPC `FailedPrecondition`; `ErrVersionConflict` -> HTTP `409 Conflict` / gRPC `Aborted`; `ErrDeltaLagNotZero` -> HTTP `409 Conflict` / gRPC `FailedPrecondition`; `ErrInvalidStateTransition` -> HTTP `409 Conflict` / gRPC `FailedPrecondition`) with sanitized external error payloads and clean connection draining on shutdown.

---

## 3. Pragmatic Hexagonal Folder Structure

```
rag-kb-catalog-service/
├── cmd/
│   └── catalog-service/
│       └── main.go                      # Dependency wiring, DB pool init, graceful shutdown
├── api/
│   ├── proto/v1/
│   │   ├── catalog.proto                # Core KB lifecycle & pointer management RPCs
│   │   └── types.proto                  # Shared domain models, states, and error types
│   └── openapi/
│       └── openapi.yaml                 # Public administrative REST specifications (OpenAPI 3.1)
├── internal/
│   ├── domain/                          # Core domain entities and invariants (no third-party I/O)
│   │   ├── entity/
│   │   │   ├── knowledge_base.go        # Aggregate root, state machine, and pointer state
│   │   │   ├── migration_state.go       # Blue-Green lifecycle & delta-write policy rules
│   │   │   ├── model_reference.go       # Validated model identifier and dimension constraints
│   │   │   └── taxonomy.go              # Hierarchical key-value namespace parser
│   │   ├── repository/                  # Repository interfaces (ports)
│   │   │   ├── catalog_repository.go
│   │   │   └── outbox_repository.go
│   │   ├── policy/                      # Invariant guards
│   │   │   ├── immutability_guard.go    # Rejects in-place vector dimension/metric modifications
│   │   │   └── access_evaluator.go      # Evaluates administrative RBAC permissions
│   │   └── event/
│   │       └── events.go                # Domain lifecycle events emitted to outbox
│   ├── application/                     # Use-case coordination
│   │   ├── command/                     # State mutations
│   │   │   ├── create_knowledge_base.go
│   │   │   ├── update_knowledge_base.go
│   │   │   ├── trigger_reindexing.go    # Initiates Blue-Green state & sets dual-write policy
│   │   │   ├── promote_pointer.go       # Checks delta lag, updates pointer, bumps version
│   │   │   ├── abort_reindexing.go      # Aborts migration, locks Green pointer, emits Blue cleanup event
│   │   │   └── delete_knowledge_base.go # Soft-deletes and writes purge event to outbox
│   │   ├── query/                       # Read workflows
│   │   │   ├── get_knowledge_base.go    # Direct Redis read with DB fallback
│   │   │   └── list_knowledge_bases.go  # Filtered multi-entity queries
│   │   ├── dto/                         # Application DTOs
│   │   └── workflow/                    # External orchestration triggers
│   │       └── ingestion_trigger.go     # Initiates backfill workflow via Temporal client
│   ├── infrastructure/                  # Adapters and concrete implementations
│   │   ├── persistence/
│   │   │   ├── postgres/
│   │   │   │   ├── pool.go              # PgBouncer-compatible connection pool setup (exec mode)
│   │   │   │   ├── catalog_repo.go      # SQL implementations enforcing optimistic locking
│   │   │   │   └── outbox_repo.go       # Writes event payloads to outbox table
│   │   │   └── migrations/              # Versioned SQL migration files
│   │   ├── cache/
│   │   │   └── redis/
│   │   │       └── cluster_client.go    # Redis Cluster client with versioned-key helpers
│   │   └── temporal/
│   │       └── client.go                # Temporal client wrapper
│   └── transport/                       # Inbound protocols
│       ├── http/
│       │   ├── router.go                # Standard HTTP routing + OpenAPI /docs & probes
│       │   ├── handler.go               # Administrative REST endpoints
│       │   └── middleware/              # Tenant extraction, JWT validation, secured logging
│       └── grpc/
│           ├── catalog_server.go        # High-performance gRPC control plane implementation
│           └── interceptors/            # Tracing, authentication, and error mapping
├── deployments/
│   ├── helm/                            # Kubernetes deployment charts (PDB, anti-affinity)
│   └── docker/
│       └── Dockerfile                   # Multi-stage distroless Go build (static:nonroot)
├── test/
│   ├── unit/                            # Pure domain logic and immutability tests
│   ├── integration/                     # Testcontainers suites (PostgreSQL + Redis Cluster)
│   └── chaos/                           # Concurrency conflict and migration failure scenarios
├── Makefile
├── go.mod
└── go.sum
```

---

## 4. Disaggregated Hybrid Ecosystem & Naming Convention Matrix

Per `My Naming Convention.pdf`, `Disaggregated Hybrid Model Architecture.pdf`, and `Go and Python Microservices Architecture.pdf`, `rag-kb-catalog-service` operates within a 12-service polyglot topology:

| Subsystem Architecture Entry | Canonical Deployment Target (`rag-*`) | Runtime | Interaction with `rag-kb-catalog-service` |
| :--- | :--- | :--- | :--- |
| **Knowledge Base Catalog** | `rag-kb-catalog-service` | Go 1.23+ | **This service**: Control-plane authority for KB CRUD, OCC versioning, static model validation, taxonomy discovery, Blue-Green migration state machine, and transactional CDC outbox. |
| **Search Orchestrator** | `rag-search-orchestrator` | Go | Calls `GetKnowledgeBase` over gRPC / reads Redis Cluster (`kb:{tenant_id}:{id}:v{ver}`) to resolve `active_index_pointer`, `retrieval_profile`, and `version` (`CorpusVersion` in `CacheKey = Hash(QueryVector + TenantID + CorpusVersion)`). Owns Tier 1/2 caches and Roaring Bitmap ACLs. |
| **Pipeline Coordinator** | `rag-ingestion-coordinator` | Go | Triggered via Temporal client (`ingestion_trigger.go`) on `TriggerReindexing` to run Blue backfill DAGs; reads `routing_policy` (`GREEN_ONLY` vs. `DUAL_WRITE`) from Redis Cluster. |
| **Chunk Lifecycle Service** | `rag-chunk-lifecycle-service` | Go | Consumes `KBDeleted`, `PointerPromoted`, and `ReindexAborted` CDC outbox events from Kafka to execute asynchronous storage compaction and index purge sagas. |
| **Notification Dispatch Daemon** | `rag-notification-dispatch-daemon` | Go | Consumes `KBCreated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, and `KBDeleted` CDC outbox events to stream SSE/WebSocket status updates and trigger `rag-webhook-delivery-worker`. |
| **Telemetry & Audit Daemon** | `rag-telemetry-audit-daemon` | Go | Records immutable HIPAA/SOC2 audit trails and corpus telemetry alongside Prometheus/ClickHouse (per `ADR-006`). |
| **Prompt Registry Service** | `rag-prompt-registry-service` | Go | Independent vertical control-plane service for immutable prompt templates. |
| **Embedding & Reranker Pools** | `rag-inference-serving` | Python (Triton/vLLM) | Executes GPU inference for models validated against the catalog's static model registry (`ADR-006`). |
| **Document AI Parsers** | `rag-document-ai-worker` | Python (GPU) | Dispatched by `rag-ingestion-coordinator` during initial ingestion and Blue-Green backfills. |
| **Tabular SQL Engine** | `rag-tabular-analytics-worker` | Python (DuckDB) | Sandboxed Parquet analytics worker; decoupled from catalog control plane. |
| **Evaluation & Quality Worker** | `rag-eval-governance-worker` | Python | Offline Ragas/TruLens evaluation and PII masking worker. |
| **Webhook / Trace Workers** | `rag-webhook-delivery-worker` / `rag-trace-collector-worker` | Python | Outbound HMAC webhook egress and OpenTelemetry provenance graph assembly. |

---

## 5. Architectural Decision Records (ADRs)

- **ADR-001: Vector Schema Immutability & Re-Indexing State Machine**: Forbids in-place vector index schema mutations (`ErrStructuralMutationDisallowed`). Any dimension, model, or distance metric upgrade must execute via a tracked Blue-Green migration state machine (`ACTIVE` -> `REINDEXING` -> `PROMOTED` / `ABORTED`).
- **ADR-002: Replay Buffer & Shared Write-Fence Protocol for Live Re-Indexing Writes**: Specifies that while a knowledge base is in `REINDEXING` status, write ingress routes live documents to both Green and a replay buffer for Blue (`DUAL_WRITE`). To prevent TOCTOU races where new writes commit between observing zero lag and switching the pointer, cutover enforces a **shared ingestion write fence (quiesce-and-drain high-water mark)**: every ingestion writer checks the KB write-fence epoch/high-water mark in Redis/PostgreSQL; prior to promotion, the migration coordinator activates the write fence (quiescing new `DUAL_WRITE` mutations or holding them at the fence barrier), drains all pre-fence delta records into Blue until the server's authoritative `kb_migrations.delta_lag_records` (`kb_catalog_migration_delta_lag_records`) equals `0`, and atomically executes `PromoteIndexPointer` under OCC (`version = version + 1`) so zero writes can commit between the final `lag == 0` check and pointer promotion.
- **ADR-003: Versioned Redis Keys Over In-Process L1 Invalidation**: Eliminates in-process memory caching and Redis Pub/Sub invalidation buses to prevent silent split-brain conditions. Standardizes on direct Redis Cluster reads using monotonic version keys (`kb:{tenant_id}:{id}:v{version}`).
- **ADR-004: Optimistic Concurrency Control Over Pessimistic Row Locking**: Enforces an integer `version` check (`version = version + 1`) on all updates to avoid row-level lock contention and deadlocks under PgBouncer transaction-mode connection pooling.
- **ADR-005: Outbox Tailing Delegated Exclusively to Debezium**: Removes internal application-level outbox polling loops. The service commits event records to an `outbox_events` table within the same PostgreSQL transaction; Debezium captures changes directly from the PostgreSQL WAL.
- **ADR-006: Exclusion of Dynamic Model Registry and Telemetry Aggregation**: Reconciles the platform-wide specification with this microservice's strict boundaries by deferring dynamic hardware inference definitions, VLM tokenizers, and cost tiers to LLM gateways/model routers (retaining only a fast, declarative **Static Validated Model Registry** in `model_reference.go`), while routing corpus runtime statistics (byte footprints, token counts, fragmentation ratios) to Prometheus and ClickHouse.
