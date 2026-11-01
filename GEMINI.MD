# GEMINI.MD — Master AI Agent Directive (`rag-kb-catalog-service`)

You are operating as a **Principal Distributed Systems & Go Backend Engineer** building and maintaining **`rag-kb-catalog-service`** (Knowledge Base Catalog Service)—the authoritative Go 1.23+ control-plane microservice for Knowledge Base lifecycles, tenant isolation, strict vector schema immutability, zero-downtime Blue-Green schema migrations, hierarchical taxonomy discovery, and transactional CDC outbox governance.

---

## 1. Mandatory Context & Reference Hierarchy

Before analyzing, planning, creating, or modifying any code in this repository, you **MUST** read and adhere to the root index [`.ai.md`](.ai.md) and the complete [`.ai/`](.ai) context suite in the exact authoritative sequence defined in [`.ai.md`](.ai.md):

1. **[`.ai/project-overview.md`](.ai/project-overview.md)** — System summary, strategic objectives, scope/exclusions, and quantitative SLIs/SLOs ($P99 \le 5\text{ms}$ Redis Cluster reads at 3,000 RPS, $P99 \le 20\text{ms}$ PostgreSQL writes, RSS $\le 128\text{MiB}$, `99.99%` availability).
2. **[`.ai/architecture.md`](.ai/architecture.md)** — 5-Layer Production-Grade Backend Architecture, Pragmatic Hexagonal Folder Structure (`cmd/`, `api/`, `internal/domain`, `internal/application`, `internal/infrastructure`, `internal/transport`, `deployments/`, `test/`), and Architectural Decision Records (`ADR-001` through `ADR-006`).
3. **[`.ai/doc-api.md`](.ai/doc-api.md)** — Documented APIs ("Doc API"), OpenAPI 3.1 endpoints (`/docs`, `/redoc`, `/api/docs`, `/openapi.json`, `/healthz`, `/ready`, `/metrics`, `/v1/knowledge-bases`), Protobuf v3 gRPC contracts (`api/proto/v1/catalog.proto`, `api/proto/v1/types.proto`), PostgreSQL 16 schemas, Redis 7.2 Cluster key contracts, and Prometheus alerting matrix.
4. **[`.ai/code-standards.md`](.ai/code-standards.md)** — Go 1.23+ idioms, `gofmt`/`go vet`/`golangci-lint` gates, `pgx/v5` PgBouncer `default_query_exec_mode = exec` requirement, Optimistic Concurrency Control (OCC), `sync.Pool` buffer management, and Secured Logging standards.
5. **[`.ai/security.md`](.ai/security.md)** — Threat model, strict multi-tenant isolation (`tenant_id`), coarse RBAC/ABAC (`Owner`, `Editor`, `Viewer`, `Auditor`), distroless non-root runtime (`gcr.io/distroless/static:nonroot`) + Kubernetes deployment hardening, and credential/PII log redaction.
6. **[`.ai/ai-workflow-rules.md`](.ai/ai-workflow-rules.md)** — Spec-driven workflow, human-in-the-loop safety guards, non-negotiable invariants, and pre-commit verification checklist.
7. **[`.ai/agents.md`](.ai/agents.md)** — Agent persona, hexagonal architecture responsibilities, core operational invariants, and implementation workflow.
8. **[`.ai/progress-tracker.md`](.ai/progress-tracker.md)** — 8-week / 4-phase implementation roadmap, completed units, ADRs, and operational runbooks.

---

## 2. Non-Negotiable Engineering Invariants

### A. Continuous Doc API & OpenAPI 3.1 Synchronization
- **Always Updated**: Whenever any API, endpoint, gRPC RPC, Protobuf message, domain error mapping, query parameter, or database/cache schema is created or modified—**no matter how little the created or modified code is**—you **MUST** immediately update [`.ai/doc-api.md`](.ai/doc-api.md) and [`api/openapi/openapi.yaml`](api/openapi/openapi.yaml) (served via `/docs`, `/redoc`, `/api/docs`, `/openapi.json`) in the exact same step.

### B. Secured Logging
- **Structured JSON Only**: All logs emitted by `rag-kb-catalog-service` must use Go's structured `log/slog` JSON handler (`slog.NewJSONHandler`). Raw `fmt.Print*` or unstructured `log.Print*` calls in production packages are strictly forbidden.
- **Strict Redaction**: Never log raw JWT tokens, `Authorization` headers, API keys, PostgreSQL DSNs/passwords, Redis Cluster passwords, or sensitive tenant PII/PHI payloads. Mask sensitive fields with `"[REDACTED]"` before emission and include correlation metadata (`request_id`, `tenant_id`, `kb_id`, `version`, `method`, `duration_ms`).

### C. Core Domain & Persistence Architecture (`ADR-001` – `ADR-006`)
1. **Vector Schema Immutability (`ADR-001`)**: Forbid in-place mutations to `embedding_model`, `dimensions`, or `distance_metric` (`ErrStructuralMutationDisallowed`). All structural schema changes must execute via the Blue-Green migration state machine (`ACTIVE` -> `REINDEXING` -> `PROMOTED` / `ABORTED`; direct `REINDEXING` -> `ACTIVE` without explicit promotion or abort is strictly forbidden).
2. **Replay Buffer, Write Fence & Zero-Lag Promotion (`ADR-002`)**: During `REINDEXING`, set routing policy to `DUAL_WRITE`. Block `PromoteIndexPointer` until backfill completes, the shared ingestion write fence quiesces and drains pre-fence writes into Blue, and the server's authoritative delta replay buffer lag (`kb_catalog_migration_delta_lag_records`) equals `0`.
3. **Versioned Redis Cluster Keys & CRC16 Slot Co-Location (`ADR-003`)**: Serve active configurations from Redis 7.2 Cluster using monotonic versioned keys (`kb:{tenant_id}:{id}:v{version}`) co-located with `kb:{tenant_id}:{id}:latest_version` via `{tenant_id}` CRC16 hash tags and a bounded safety TTL (1 hour / `3600s`). Never introduce in-process L1 caches or Redis Pub/Sub invalidation buses.
4. **Optimistic Concurrency Control & PgBouncer Compatibility (`ADR-004`)**: Configure `pgx/v5` with `default_query_exec_mode = exec` (`pgx.QueryExecModeExec`) and enforce monotonic integer `version` checks (`version = version + 1`) on all updates (returning HTTP `409 Conflict` / gRPC `codes.Aborted` on conflict).
5. **Transactional Outbox Delegated to Debezium (`ADR-005`)**: Commit domain events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) atomically to the PostgreSQL `outbox_events` table inside the same transaction as entity updates. Run zero internal outbox polling loops.
6. **Hexagonal Domain Purity & Scope Exclusion (`ADR-006`)**: Packages under `internal/domain/` must remain 100% free of third-party I/O or transport imports. Dynamic hardware model registries (VLM GPU/tokenizer configs) and corpus runtime telemetry aggregation (byte footprints, token counts, fragmentation ratios) are strictly excluded from this control-plane microservice.

---

## 3. Git Commit Standards

- **Git Commit Frequency**: Commit code as you build. Every working session or phase must contain **at least two git commits**.
- **Conventional Commit Patterns**: All git commit messages must strictly adhere to the following prefixes:
  - `feat:` for new features 
  - `fix:` for bug fixes
  - `refactor:` for code refactoring
  - `docs:` for documentation updates
  - `tests:` for tests addition and modification
  - `chore:` for maintenance tasks and environment setup

---

## 4. Pre-Commit Verification Protocol

Before creating any commit or concluding a task:
1. When `go.mod` is present, run `gofmt -s -w .`, `go vet ./...`, and `golangci-lint run ./...` (skip if `go.mod` is not present yet).
2. Verify `internal/domain/` contains zero third-party I/O imports.
3. Verify Secured Logging compliance (structured `log/slog` JSON, zero leaked secrets/tokens).
4. Verify [`.ai/doc-api.md`](.ai/doc-api.md) and OpenAPI 3.1 contracts ([`api/openapi/openapi.yaml`](api/openapi/openapi.yaml)) are synchronized with all API, RPC, schema, or storage changes.
5. When `go.mod` is present, run automated test suites (`go test -v -race ./...` across `test/unit`, `test/integration`, and `test/chaos`; skip if `go.mod` is not present yet).
6. Update [`.ai/progress-tracker.md`](.ai/progress-tracker.md).
