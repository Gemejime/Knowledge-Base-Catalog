# AI Workflow Rules - RAG Knowledge Base Catalog Service

## 1. Spec-Driven Development Approach

Build `rag-kb-catalog-service` incrementally following a strict spec-driven workflow:

- The specification documents in `tasks/` (especially `RAG KB Catalog Service Implementation Plan.pdf` and `Production Grade Backend Concepts.pdf`) and context files in [`.ai/`](.ai) define all system invariants, hexagonal boundaries, data schemas, and operational SLIs/SLOs.
- Adhere strictly to the Pragmatic Hexagonal Folder Structure (`cmd/catalog-service`, `api/proto/v1`, `api/openapi`, `internal/domain`, `internal/application`, `internal/infrastructure`, `internal/transport`, `deployments`, `test`) and Architectural Decision Records (`ADR-001` through `ADR-006`).
- Do not introduce out-of-scope features such as in-process L1 caches, Redis Pub/Sub invalidation buses, application-level outbox polling loops, dynamic hardware/VLM model registries, or in-process Roaring Bitmap evaluations.

---

## 2. Human-in-the-Loop & Safety Guards

- **Plan First**: Create an implementation prompt in `prompts/<task-name>.md` detailing goals, files to modify, security, and verification steps and ask for clarification if requirements are ambiguous.
- **Clarify Ambiguities**: If business rules, RBAC permissions, or migration state transitions are ambiguous, ask for clarification before making assumptions.
- **Destructive Action Guards**: Always request human confirmation before executing destructive SQL operations, dropping tables, or force-resetting Git history.
- **Demonstrate & Verify**: After completing each unit of work, run formatting, linting, static analysis, secured logging checks, OpenAPI/Doc API synchronization checks, and automated tests.

---

## 3. Non-Negotiable Invariants

1. **Continuous Doc API & OpenAPI 3.1 Synchronization**: Both [`.ai/doc-api.md`](.ai/doc-api.md) and `api/openapi/openapi.yaml` (served via `/docs`, `/redoc`, `/api/docs`, `/openapi.json`) MUST be updated on **every code change or addition**, no matter how small the created or modified code is.
2. **Secured Logging**: All application logs must be structured JSON (`log/slog`). Strictly redact JWT tokens, `Authorization` headers, database DSNs/passwords, Redis credentials, and PII/PHI before emission. Raw `fmt.Print*` and `log.Print*` calls in production code are prohibited.
3. **Vector Schema Immutability (ADR-001)**: Reject any in-place modification to `embedding_model`, `dimensions`, or `distance_metric` with `ErrStructuralMutationDisallowed`. All schema upgrades must transition through the Blue-Green migration state machine (`ACTIVE` -> `REINDEXING` -> `PROMOTED` / `ABORTED`).
4. **Zero-Lag Promotion Gate (ADR-002)**: While in `REINDEXING` state, ingestion routing policy is `DUAL_WRITE`. `PromoteIndexPointer` is strictly blocked until backfill completes and delta replay buffer lag equals `0`.
5. **Versioned Redis Cluster Caching (ADR-003)**: Read active configurations directly from Redis 7.2 Cluster using monotonic versioned keys (`kb:{tenant_id}:{id}:v{version}`) with bounded safety TTLs. Never use in-process L1 caches or Pub/Sub invalidation.
6. **Optimistic Concurrency Control & PgBouncer Exec Mode (ADR-004)**: Configure `pgx/v5` with `default_query_exec_mode = exec` (`pgx.QueryExecModeExec`) for PgBouncer compatibility, and guard all entity updates with monotonic integer `version` checks (`409 Conflict` / gRPC `Aborted` on mismatch).
7. **Transactional Outbox Delegated to Debezium (ADR-005)**: Commit domain events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) to the PostgreSQL outbox table in the same transaction as entity updates; run zero internal outbox polling loops.
8. **Git Commit Standards**:
   - **Git Commit Frequency**: Commit code as you build. Every working session or phase must contain **at least two git commits**.
   - **Conventional Commit Patterns**: All git commit messages must strictly adhere to the following prefixes:
     - `feat:` for new features
     - `fix:` for bug fixes
     - `refactor:` for code refactoring
     - `docs:` for documentation updates
     - `tests:` for tests addition and modification
     - `chore:` for maintenance tasks and environment setup

---

## 4. Pre-Commit Verification Checklist

Before creating a commit or marking a phase/task complete:

1. **Formatting**: Run `gofmt -s -w .` and verify clean formatting.
2. **Static Analysis & Linting**: Run `go vet ./...` and `golangci-lint run ./...`.
3. **Domain Isolation Check**: Confirm `internal/domain/` has zero third-party I/O imports.
4. **Secured Logging Audit**: Confirm structured `log/slog` JSON usage and zero leaked secrets/tokens.
5. **Doc API & OpenAPI Sync**: Confirm [`.ai/doc-api.md`](.ai/doc-api.md) and `api/openapi/openapi.yaml` reflect every created or modified endpoint, RPC, schema, or error code.
6. **Test Suites**: Run `go test -v -race ./...` across `test/unit`, `test/integration`, and `test/chaos`.
7. **Progress Tracker**: Update [`.ai/progress-tracker.md`](.ai/progress-tracker.md).
