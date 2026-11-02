# Implementation Prompt: Phase 3 — Blue-Green State Machine & Ingestion Handshake

## 1. Objective & Scope

Implement **Phase 3 (Weeks 5–6): Blue-Green State Machine & Ingestion Handshake** for `rag-kb-catalog-service` (Go 1.23+), strictly adhering to [`GEMINI.MD`](../GEMINI.MD), [`.ai.md`](../.ai.md), the [`.ai/`](../.ai/) specification suite, and `tasks/RAG KB Catalog Service Implementation Plan.pdf`.

### Core Architectural Invariants for Phase 3:
1. **Re-Indexing Lifecycle State Machine (`ADR-001`)**:
   - `ACTIVE` -> `REINDEXING`: Triggered via `TriggerReindexing`. Sets `routing_policy = DUAL_WRITE`, allocates Blue index pointer, records `kb_migrations` with `state = 'REINDEXING'`, and writes `ReindexInitiated` outbox event.
   - `REINDEXING` -> `PROMOTED`: Triggered via `PromoteIndexPointer`. Atomic cutover gated on verified zero delta lag (`kb_catalog_migration_delta_lag_records == 0`), backfill completion, and ingestion write-fence drain. Post-promotion aggregate operational state settles at `ACTIVE` with Blue promoted to `active_index_pointer`, candidate pointer cleared, and `routing_policy` reset to `GREEN_ONLY`.
   - `REINDEXING` -> `ABORTED`: Triggered via `AbortReindexing`. Rollback to Green pointer, candidate pointer cleared, `routing_policy` reset to `GREEN_ONLY`, state settles at `ACTIVE`, and `ReindexAborted` cleanup event emitted to outbox to purge orphaned Blue index.
   - Direct `REINDEXING` -> `ACTIVE` without promotion or abort is strictly forbidden (`ErrInvalidStateTransition`).
2. **Temporal Go SDK Client Integration (`ingestion_trigger.go`)**:
   - Implement `internal/infrastructure/temporal/client.go` implementing `workflow.IngestionTrigger`.
   - Schedules asynchronous backfill DAG in `rag-ingestion-coordinator`.
   - Fail-closed error handling and configurable retry policies.
3. **Atomic Write-Fence & Zero-Lag Promotion Gate (`ADR-002`)**:
   - Reads authoritative `delta_lag_records` from PostgreSQL `kb_migrations` (never trusting client assertion alone).
   - Blocks promotion if `delta_lag_records > 0`, returning `ErrDeltaLagNotZero` (HTTP `409 Conflict` / gRPC `codes.FailedPrecondition`).
4. **Chaos Testing & Failure Resiliency (`test/chaos/`)**:
   - Simulate backfill worker crash / stalled replay buffer and verify rollback via `AbortReindexing` keeps Green index intact.
   - Simulate lag spikes and verify `PromoteIndexPointer` precondition failure.

---

## 2. Component Specifications & Files to Create / Modify

### A. Temporal Client Adapter (`internal/infrastructure/temporal/client.go`)
- Create `TemporalClient` wrapping `go.temporal.io/sdk/client`.
- Implement `workflow.IngestionTrigger` interface:
  - `TriggerBackfillDAG(ctx context.Context, req BackfillTriggerRequest) (string, error)`
- Connect to Temporal server using environment variables (`TEMPORAL_HOST_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_TASK_QUEUE`).
- Wire gracefully in `cmd/catalog-service/main.go`.

### B. Delta Lag Reconciliation & Ingestion Coordination
- Provide application/infrastructure hooks to simulate or report delta lag updates from ingestion workers.
- Verify `PromoteIndexPointer` OCC validation and lag threshold gate.
- Verify `AbortReindexing` emits `ReindexAborted` event for outbox CDC streaming.

### C. Chaos & Migration Failure Tests (`test/chaos/`)
- `test/chaos/migration_failure_test.go`:
  - Scenario 1: Backfill job finishes but incoming live writes create a lag of 50 unprocessed chunks in replay buffer. Verify `PromotePointer` returns precondition failure (`ErrDeltaLagNotZero`).
  - Scenario 2: Worker failure in `rag-ingestion-coordinator`. Trigger `AbortReindexing`; verify search pointer remains pinned to Green, state resets to `ACTIVE`, and cleanup event is dispatched to outbox to purge orphaned Blue index.
  - Scenario 3: Concurrent OCC conflict during pointer promotion.

---

## 3. Verification Checklist

1. `gofmt -s -w .`, `go vet ./...`, `golangci-lint run ./...`
2. Domain layer purity: zero third-party I/O imports in `internal/domain/`.
3. Secured logging: all Temporal connection details redact secrets.
4. Continuous Doc API & OpenAPI 3.1 synchronization (`.ai/doc-api.md`, `api/openapi/openapi.yaml`).
5. All test suites (`unit`, `chaos`) pass with `-race`.

