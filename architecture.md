# Architecture Specification — `rag-kb-catalog-service`

The **`rag-kb-catalog-service`** is the authoritative control-plane metadata microservice and system of record for Knowledge Base (KB) lifecycles, tenant isolation, and retrieval configurations within a Disaggregated Hybrid Multimodal RAG architecture.

---

## 1. Five-Layer Pragmatic Hexagonal Architecture

The microservice follows a strict Hexagonal / Ports & Adapters architecture designed to isolate business rules from persistence, transport protocols, and cloud platforms.

```
                           ┌──────────────────────────────────────────────┐
                           │               Transport Layer                │
                           │   - HTTP/REST (/v1/knowledge-bases)          │
                           │   - gRPC HTTP/2 (CatalogService)             │
                           │   - Middleware (Auth, Correlation, Slog)     │
                           └──────────────────────┬───────────────────────┘
                                                  │
                                                  ▼
                           ┌──────────────────────────────────────────────┐
                           │              Application Layer               │
                           │   - Commands (Create, Update, Promote...)    │
                           │   - Queries (Get, List with Taxonomy)        │
                           │   - Ports / Repository Interfaces            │
                           └──────────────────────┬───────────────────────┘
                                                  │
                                                  ▼
                           ┌──────────────────────────────────────────────┐
                           │                 Domain Layer                 │
                           │   - Entities (KnowledgeBase, Migration)      │
                           │   - State Machine (ACTIVE -> REINDEXING)     │
                           │   - Invariant Guards (Schema Immutability)   │
                           │   * 100% Pure Go: Zero External I/O Imports  │
                           └──────────────────────▲───────────────────────┘
                                                  │
                           ┌──────────────────────┴───────────────────────┐
                           │             Infrastructure Layer             │
                           │   - PostgreSQL 16 (pgx/v5, exec mode)        │
                           │   - Redis 7.2 Cluster (Versioned keys)       │
                           │   - Temporal Client (DAG backfill trigger)   │
                           │   - Prometheus Metrics & Alerting            │
                           └──────────────────────────────────────────────┘
```

### Layer Responsibilities

1. **Domain Layer (`internal/domain/`)**:
   - Contains pure business logic, entities, value objects, domain errors, and state machine transitions.
   - **Architectural Rule**: Must never import third-party libraries, databases, HTTP, or gRPC packages.
   - Enforces **Vector Schema Immutability** (`immutability_guard.go`), ensuring physical vector configurations cannot be mutated in place.

2. **Application Layer (`internal/application/`)**:
   - Coordinates application use cases via Command and Query orchestrators (`command/`, `query/`).
   - Defines domain repository interfaces (Ports) implemented by the Infrastructure layer.
   - Enforces Optimistic Concurrency Control (OCC) version increments (`version = version + 1`) and causal freshness checks.

3. **Infrastructure Layer (`internal/infrastructure/`)**:
   - **Persistence (`persistence/postgres/`)**: PostgreSQL 16 implementation using `jackc/pgx/v5` with PgBouncer compatibility (`QueryExecModeExec`). Executes atomic entity updates and outbox insertions within single transactions.
   - **Caching (`cache/redis/`)**: Redis 7.2 Cluster adapter managing monotonic versioned keys (`kb:{tenant_id}:{id}:v{version}`) and latest version pointers.
   - **Workflows (`temporal/`)**: Triggers asynchronous re-indexing and backfill workflows via Temporal Ingestion Coordinator.
   - **Observability (`observability/`)**: Prometheus metrics collector and structured `log/slog` logging with PII masking.

4. **Transport Layer (`internal/transport/`)**:
   - **HTTP Transport (`http/`)**: REST handlers mounted via Go standard library ServeMux, serving OpenAPI 3.1 contracts (`/openapi.json`), Swagger UI (`/docs`), Redoc (`/redoc`), and operational probes (`/healthz`, `/ready`).
   - **gRPC Transport (`grpc/`)**: Protobuf v3 service handlers implementing `CatalogService`.
   - **Middlewares & Interceptors**: Authentication (JWT HMAC), RBAC/ABAC policy enforcement, request tracing (`X-Request-ID`), and secured structured logging.

5. **Entrypoint & Initialization (`cmd/catalog-service/`)**:
   - Wires concrete infrastructure adapters into application services and starts HTTP/gRPC servers with graceful signal trapping (`SIGINT`, `SIGTERM`).

---

## 2. Blue-Green Schema Migration Protocol (ADR-001 & ADR-002)

Physical vector indexes (embedding models, dimension counts, distance metrics) cannot be altered in-place without corrupting existing vector distances and retrieval topologies. The service enforces an automated Blue-Green migration state machine:

```
                  ┌──────────────┐
                  │    ACTIVE    │
                  └──────┬───────┘
                         │ TriggerReindexing
                         ▼
                  ┌──────────────┐
        ┌─────────┤  REINDEXING  ├─────────┐
        │         └──────────────┘         │
        │ AbortReindexing   │ PromoteIndex │ (Gated on lag = 0)
        ▼                   ▼              ▼
 ┌──────────────┐                  ┌──────────────┐
 │   ABORTED    │                  │   PROMOTED   │
 └──────────────┘                  └──────────────┘
```

### Migration Workflow

1. **Trigger Phase**:
   - Client initiates `TriggerReindexing` with a new target schema (`green_model`, `green_dimension`, `green_distance_metric`).
   - Migration record is committed atomically to `kb_migrations` with initial status `REINDEXING`.
   - Active index routing rule switches to `DUAL_WRITE`.
   - Client schedules backfill pipeline in Temporal.

2. **Dual-Write Ingestion & Replay Buffer**:
   - During `REINDEXING`, new document writes stream simultaneously to Blue (current) and Green (target).
   - In-flight writes are tracked in a delta replay buffer.

3. **Atomic Promotion Phase (`PromoteIndexPointer`)**:
   - Promotion is strictly blocked until:
     1. Asynchronous backfill completes.
     2. Ingestion write fence quiesces and drains pre-fence writes into Blue.
     3. Delta replay buffer lag (`kb_catalog_migration_delta_lag_records`) drops to **0**.
   - Upon zero-lag verification, the service flips `active_index_pointer` from `blue` to `green` and transitions state to `PROMOTED`.

4. **Abort & Rollback Phase (`AbortReindexing`)**:
   - If backfill fails or stalls, `AbortReindexing` transitions state to `ABORTED`, shuts down dual-write routing, and reverts ingress entirely to Blue without downtime.

---

## 3. Versioned Caching & CRC16 Co-Location (ADR-003)

To deliver $P99 \le 5\text{ms}$ read latency at 3,000 RPS without volatile in-process caches or distributed invalidation buses:

- Active configurations are cached in Redis 7.2 Cluster under versioned keys:
  ```
  kb:{tenant_id}:{id}:v{version}
  kb:{tenant_id}:{id}:latest_version
  ```
- **CRC16 Hash Slot Co-Location**:
  Using the `{tenant_id}` hash tag guarantees that both the versioned entity payload and its latest version pointer reside on the same Redis Cluster node, allowing atomic multi-key operations and pipelined lookups.
- **Causal Read Consistency**:
  Reads specifying `min_version` verify whether `cached_version >= min_version`. If the cached version is stale (e.g. following an immediate mutation), the service automatically falls back to PostgreSQL, providing bounded read-your-writes causal consistency.

---

## 4. Optimistic Concurrency Control & PgBouncer (ADR-004)

- **Optimistic Concurrency Control (OCC)**:
  Every Knowledge Base record carries a monotonic integer `version` column. Mutations execute conditional updates:
  ```sql
  UPDATE knowledge_bases
  SET description = $3, version = version + 1, updated_at = NOW()
  WHERE tenant_id = $1 AND id = $2 AND version = $4;
  ```
  If zero rows are updated, an OCC conflict is identified and returned as HTTP `409 Conflict` / gRPC `codes.Aborted`.

- **PgBouncer Transaction-Mode Pooling**:
  PgBouncer in transaction mode does not support session-level prepared statements. The Go PostgreSQL driver (`pgx/v5`) is explicitly initialized with:
  ```go
  config.DefaultQueryExecMode = pgx.QueryExecModeExec
  ```
  This eliminates prepared statement cache collisions across multiplexed pooled connections.

---

## 5. Transactional CDC Outbox Architecture (ADR-005)

To prevent dual-write inconsistencies between PostgreSQL and Apache Kafka:

1. Application updates commit domain events directly to an `outbox_events` table within the same SQL transaction:
   ```sql
   INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, payload, tenant_id, created_at)
   VALUES ($1, 'knowledge_base', $2, $3, $4, $5, $6);
   ```
2. **Zero Internal Polling**: The Go microservice runs no internal worker loops or polling queries against `outbox_events`.
3. **Debezium CDC Connector**:
   Debezium tails the PostgreSQL Write-Ahead Log (WAL) via the `pgoutput` plugin and reliably streams events to Kafka with at-least-once delivery semantics.

---

## 6. Security & Multi-Tenant Isolation

- **Tenant Boundary Enforcement**:
  Every database query and cache key includes `tenant_id` as the primary partition key (`WHERE tenant_id = $1 AND id = $2`). Cross-tenant access is structurally impossible.
- **RBAC & ABAC Roles**:
  - `Owner`: Full management, creation, updates, and soft deletion.
  - `Editor`: Creation, metadata updates, and migration orchestration.
  - `Viewer`: Read-only access to catalog definitions within tenant scope.
  - `Auditor`: Read-only access with audit log visibility.
- **Distroless Non-Root Runtime**:
  The microservice runs in a minimal `gcr.io/distroless/static:nonroot` container image as UID 65532 under Kubernetes Pod Security Standards (Restricted) with a read-only root filesystem and dropped capabilities.
