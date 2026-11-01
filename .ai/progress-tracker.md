# Progress Tracker - RAG Knowledge Base Catalog Service

## Current Phase

- **Phase 0 / Phase 1 Bootstrap: Context Architecture, CI/CD Governance & Repository Foundation** (🟢 Completed)

## Current Goal

- Establish `.gitignore` (excluding `tasks/`), GitHub Actions CI/CD workflow (`.github/workflows/ci.yml`), `.ai/` & `.ai.md` architectural context aligned with `RAG KB Catalog Service Implementation Plan.pdf` and `Production Grade Backend Concepts.pdf`, and `GEMINI.MD` master agent directive. Ready to begin **Phase 1: Core Schemas & Concurrency Controls**.

---

## Streamlined 8-Week Timeline & Milestones

| Phase | Duration | Core Domain Focus | Deliverables & Milestone Exit Criteria | Status |
| :--- | :--- | :--- | :--- | :--- |
| **Phase 1: Core Schemas & Concurrency Controls** | Weeks 1–2 | Relational Schemas & Optimistic Locking | • PostgreSQL 16 schema with strict `tenant_id` isolation, monotonic integer `version` columns, and `outbox_events` tables.<br>• Domain models enforcing immutable vector schema rules (`ErrStructuralMutationDisallowed`).<br>• gRPC (`catalog.proto`, `types.proto`) and OpenAPI 3.1 REST endpoints (`openapi.yaml`, `/docs`) with PgBouncer-compatible connection configuration (`default_query_exec_mode = exec`). | 🟡 Ready to Start |
| **Phase 2: Read Plane & Versioned Redis Caching** | Weeks 3–4 | Direct Cache Architecture & Taxonomy | • Direct Redis 7.2 Cluster cache reads using monotonic versioned keys (`kb:{tenant_id}:{id}:v{version}`) with 1-hour safety TTL and zero L1 invalidation buses.<br>• Hierarchical key-value taxonomy parser (`namespace:key=value`) supporting query federation.<br>• Unit and Testcontainers integration suites (PostgreSQL + Redis Cluster) verifying zero cache drift across updates. | ⚪ Planned |
| **Phase 3: Migration State Machine & Ingestion Handshake** | Weeks 5–6 | Blue-Green State Machine & Delta Protocol | • Re-indexing lifecycle state machine (`ACTIVE` -> `REINDEXING` -> `PROMOTED` / `ABORTED`) supporting `DUAL_WRITE` routing rules.<br>• Temporal Go SDK client trigger integration (`ingestion_trigger.go`) for launching external backfill DAGs in `rag-ingestion-coordinator`.<br>• Promotion validation (`PromoteIndexPointer`) gating switchover on verified `0`-lag delta replay (`kb_migration_delta_lag_records == 0`). | ⚪ Planned |
| **Phase 4: CDC Verification, Chaos & Production Rollout** | Weeks 7–8 | Debezium Integration, Chaos & Load Tuning | • Verification of transactional outbox streaming (`KBCreated`, `ReindexInitiated`, `PointerPromoted`, `KBDeleted`) via Debezium WAL capture to Apache Kafka.<br>• Chaos tests validating rollback to Green upon backfill failure or stalled delta buffers (`AbortReindexing`).<br>• Load benchmarking verifying $P99 \le 5\text{ms}$ reads and $P99 \le 20\text{ms}$ writes at 3,000 RPS within $\le 128\text{MiB}$ RSS per pod. | ⚪ Planned |

---

## Completed Units

- **Repository Governance & CI/CD Setup**:
  - **Git Ignore Configuration** ([`.gitignore`](.gitignore)):
    - Added exclusion for `tasks/` reference specification folder, Go build binaries, coverage artifacts, `.env` secrets, and editor files.
  - **GitHub Actions CI/CD Pipeline** ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)):
    - `lint-and-typecheck`: Validates Go 1.23 formatting (`gofmt -s`), `go vet`, `golangci-lint`, and hexagonal domain layer isolation (`internal/domain` free of third-party I/O imports).
    - `security-and-logging-audit`: Verifies `.gitignore` rules for `tasks/`, enforces structured `log/slog` JSON logging and credential/token redaction, checks `pgx` `QueryExecModeExec` for PgBouncer compatibility, and runs `govulncheck`.
    - `openapi-and-doc-api-sync`: Verifies presence and OpenAPI 3.1 / gRPC completeness of `.ai/doc-api.md` and `api/openapi/openapi.yaml`.
    - `test-suites`: Executes unit (`test/unit`), integration (`test/integration`), and chaos (`test/chaos`) test suites with the Go race detector (`-race`).
  - **AI Context & Specification Alignment** ([`.ai/`](.ai), [`.ai.md`](.ai.md), [`GEMINI.MD`](GEMINI.MD)):
    - Updated all `.ai/` context files (`project-overview.md`, `architecture.md`, `doc-api.md`, `code-standards.md`, `security.md`, `ai-workflow-rules.md`, `agents.md`, `progress-tracker.md`) and `.ai.md` to specify `rag-kb-catalog-service` in Go 1.23+ adhering to `RAG KB Catalog Service Implementation Plan.pdf` and `Production Grade Backend Concepts.pdf`.
    - Created `GEMINI.MD` referencing `.ai/agents.md` and the complete `.ai/` directory.

---

## Architectural Decision Records (ADRs)

- **ADR-001: Vector Schema Immutability & Re-Indexing State Machine**: Forbids in-place vector index schema mutations (`ErrStructuralMutationDisallowed`). Any dimension, model, or metric upgrade must execute via a tracked Blue-Green migration state machine.
- **ADR-002: Replay Buffer Protocol for Live Re-Indexing Writes**: Specifies that while a knowledge base is in `REINDEXING` status, write ingress routes live documents to both Green and a replay buffer for Blue (`DUAL_WRITE`). Atomic promotion is strictly blocked until backfill finishes and delta buffer lag reaches zero.
- **ADR-003: Versioned Redis Keys Over In-Process L1 Invalidation**: Eliminates in-process memory caching and Redis Pub/Sub invalidation buses to prevent silent split-brain conditions. Standardizes on direct Redis Cluster reads using monotonic version keys (`kb:{tenant_id}:{id}:v{version}`).
- **ADR-004: Optimistic Concurrency Control Over Pessimistic Row Locking**: Enforces an integer `version` check on all updates to avoid row-level lock contention and deadlocks under PgBouncer connection pooling.
- **ADR-005: Outbox Tailing Delegated Exclusively to Debezium**: Removes internal application-level outbox polling loops. The service commits event records to an outbox table; Debezium captures changes directly from the PostgreSQL WAL.
- **ADR-006: Exclusion of Dynamic Model Registry and Telemetry Aggregation**: Defers hardware inference definitions, tokenizers, and cost tiers to model routers, while routing corpus runtime statistics to Prometheus and ClickHouse.

---

## Operational Runbooks

### Runbook 1: Resolving a Stuck Re-Indexing Delta Buffer
1. Inspect the delta replay buffer lag metric (`kb_migration_delta_lag_records`).
2. If the lag fails to converge to `0`, throttle batch ingestion traffic for the tenant temporarily via the API Gateway rate limiter.
3. Monitor the buffer drain rate. Once lag reaches `0`, trigger `PromoteIndexPointer`.
4. If the replay worker has crashed, execute `AbortReindexing`, verify the search pointer remains pinned to Green, and clear the staging queue.

### Runbook 2: Recovering from Optimistic Lock Contention Spikes
1. Monitor `kb_catalog_optimistic_conflicts_total` in Prometheus.
2. If a specific tenant triggers elevated HTTP `409 Conflict` / gRPC `Aborted` responses, verify whether an automated administrative script is firing uncoordinated concurrent metadata updates.
3. Direct the client application to apply exponential backoff with full jitter on retries.
