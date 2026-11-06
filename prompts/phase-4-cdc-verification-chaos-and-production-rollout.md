# Implementation Prompt: Phase 4 — CDC Verification, Chaos & Production Rollout

## 1. Objective & Scope

Implement **Phase 4 (Weeks 7–8): CDC Verification, Chaos & Production Rollout** for `rag-kb-catalog-service` (Go 1.23+), strictly adhering to [`GEMINI.MD`](../GEMINI.MD), [`.ai.md`](../.ai.md), the [`.ai/`](../.ai/) specification suite, and `tasks/RAG KB Catalog Service Implementation Plan.pdf`.

### Core Architectural Invariants for Phase 4:

1. **Transactional CDC Outbox Streaming (`ADR-005`)**:
   - Atomically record state mutations in PostgreSQL 16 `outbox_events` within the same transaction as entity updates.
   - Zero internal application-level outbox polling loops or `LISTEN/NOTIFY` workers.
   - Debezium WAL capture via PostgreSQL `pgoutput` plugin with Kafka Connect Outbox Event Router SMT (`io.debezium.transforms.outbox.EventRouter`).
   - Explicit snake_case column mapping matching PostgreSQL schema (`event_id`, `tenant_id`, `aggregate_type`, `aggregate_id`, `event_type`, `created_at`, `payload`).
   - Partition ordering keyed strictly by `tenant_id` (`transforms.outbox.table.field.event.key: "tenant_id"`) to guarantee per-tenant causal ordering in Kafka without topic explosion.
   - Fixed target Kafka topic routing (`rag.catalog.events`) via regex replacement (`route.topic.regex: ".*"`, `route.topic.replacement: "rag.catalog.events"`).
   - Kafka header placement for downstream zero-payload deserialization filtering (`eventType`, `aggregateVersion`, `tenantId`) consumed by `rag-notification-dispatch-daemon` and `rag-search-orchestrator`.
   - Strict event schemas for all 6 domain events: `KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`.
2. **Chaos & Atomicity Testing Expansion**:
   - Validate outbox transaction rollback: failure during entity mutation or repository execution must guarantee zero orphaned outbox events in the database / repository.
   - Verify concurrent OCC conflict storms under load: ensure deterministic resolution with 1 success and remaining operations failing with 409 Conflict without deadlocking or corrupting the outbox stream.
   - Verify soft delete outbox atomicity: soft delete emits `KBDeleted` outbox event with clean pointer cancellation and zero dangling active migrations.
3. **Load Benchmarking & Quantitative SLI/SLO Gates**:
   - Verify Direct-Cache Read Latency SLI: $P99 \le 5\text{ms}$ at 3,000 RPS target concurrency.
   - Verify Database Mutation Latency SLI: $P99 \le 20\text{ms}$ under peak concurrent tenant writes.
   - Flat memory footprint: verify resident set size (RSS) $\le 128\text{MiB}$ per pod using `sync.Pool` pooled serialization buffers (`internal/infrastructure/observability/buffer_pool.go`) and runtime memory assertions.
   - Synthetic load test harness measuring exact latency quantiles (P50, P90, P99) alongside standard Go microbenchmarks.
4. **Production Deployment Packaging & GitOps Strategy (Approved Integration: Standard Helm Chart + Declarative ArgoCD Application Manifest)**:
   - Distroless multi-stage Docker build (`deployments/docker/Dockerfile`) running as non-root (`gcr.io/distroless/static:nonroot`, UID 65532).
   - High-Availability Kubernetes Helm Chart (`deployments/helm/rag-kb-catalog-service/`):
     - `PodDisruptionBudget` configured with `minAvailable: 2`.
     - Zone anti-affinity rules (`topology.kubernetes.io/zone`).
     - Hardened Pod Security Standards (`PSS: restricted`): `runAsNonRoot: true`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `seccompProfile: { type: "RuntimeDefault" }`.
     - Resource requests (CPU 100m, memory 64MiB) and limits (CPU 500m, memory 128MiB).
     - Multi-port service exposing HTTP REST (8080), gRPC HTTP/2 (50051), and dedicated internal metrics (9090).
     - Liveness probe (`/healthz`) and dual-dependency Readiness probe (`/ready`).
     - Prometheus alerting rules matching the 4 core failure modes with explicit PromQL formulas:
       1. High mutation conflicts (>5% of write traffic over 5m)
       2. P99 read degradation (>10ms for >3m)
       3. Re-index convergence stall (delta buffer lag $> 0$ without decrease for >15m)
       4. Database pool saturation (>85% pool capacity for >2m)
   - Declarative GitOps ArgoCD Manifest (`deployments/argocd/application.yaml`):
     - Standard Kubernetes custom resource `argoproj.io/v1alpha1` `Application`.
     - Targets `deployments/helm/rag-kb-catalog-service` with automated sync policy (`prune: true`, `selfHeal: true`).
     - Sync options: `CreateNamespace=true`, `ApplyOutOfSyncOnly=true`, `PruneLast=true`.
     - Retry policy with exponential backoff (`limit: 5`, `duration: 5s`, `factor: 2`, `maxDuration: 3m`).
     - Strict decoupling: Go application remains 100% agnostic of ArgoCD, preserving pure 12-factor cloud-native decoupling.
5. **Scope Purity & Zero AI/Decision Model Invariant (`ADR-006`)**:
   - Re-verify that no dynamic hardware model registry, tokenizer, VLM runtime, JEPA/decision AI model, or query-routing classifier is added to this microservice. All AI decision/inference models remain decoupled in `rag-search-orchestrator`, `rag-inference-serving`, and `rag-eval-governance-worker`.

---

## 2. Component Specifications & Files to Create / Modify

### A. Debezium Kafka Connect Outbox Configuration (`deployments/debezium/`)

Create `deployments/debezium/register-postgres-outbox-connector.json`:
- **Connector Name**: `rag-kb-catalog-outbox-connector`
- **Connector Class**: `io.debezium.connector.postgresql.PostgresConnector`
- **Plugin Name**: `pgoutput`
- **Database & Table Configuration**:
  - `database.hostname`: `"${POSTGRES_HOST}"`
  - `database.port`: `"${POSTGRES_PORT}"`
  - `database.user`: `"${POSTGRES_USER}"`
  - `database.password`: `"${POSTGRES_PASSWORD}"`
  - `database.dbname`: `"${POSTGRES_DB}"`
  - `database.server.name`: `"rag_catalog"`
  - `topic.prefix`: `"rag_catalog"`
  - `table.include.list`: `"public.outbox_events"`
- **Replication Slot & Publication**:
  - `publication.name`: `"rag_kb_outbox_publication"`
  - `publication.autocreate.mode`: `"filtered"`
  - `slot.name`: `"rag_kb_debezium_slot"`
  - `slot.drop.on.stop`: `"false"`
  - `tombstones.on.delete`: `"false"`
  - `decimal.handling.mode`: `"double"`
  - `time.precision.mode`: `"connect"`
- **Outbox EventRouter SMT (`io.debezium.transforms.outbox.EventRouter`)**:
  - `transforms`: `"outbox"`
  - `transforms.outbox.type`: `"io.debezium.transforms.outbox.EventRouter"`
  - `transforms.outbox.table.field.event.id`: `"event_id"`
  - `transforms.outbox.table.field.event.key`: `"tenant_id"`
  - `transforms.outbox.table.field.event.type`: `"event_type"`
  - `transforms.outbox.table.field.event.timestamp`: `"created_at"`
  - `transforms.outbox.table.field.event.payload`: `"payload"`
  - `transforms.outbox.table.expand.json.payload`: `"true"`
  - `transforms.outbox.route.topic.regex`: `".*"`
  - `transforms.outbox.route.topic.replacement`: `"rag.catalog.events"`
  - `transforms.outbox.table.fields.additional.placement`: `"event_type:header:eventType,aggregate_version:header:aggregateVersion,tenant_id:header:tenantId"`

### B. Outbox Serialization & CDC Contract Unit Tests (`test/unit/outbox_cdc_test.go`)

- **Domain Event Envelope Completeness**:
  - Verify JSON serialization and payload completeness across all 6 domain event types:
    1. `KBCreated`: `event_id`, `tenant_id`, `kb_id`, `name`, `version`, `state: ACTIVE`, `routing_policy: GREEN_ONLY`, `active_index_pointer`, `candidate_index_pointer: ""`, `active_schema`, `candidate_schema: null`, `taxonomy_tags`, `occurred_at`.
    2. `KBUpdated`: above fields with incremented version and mutated metadata.
    3. `ReindexInitiated`: above fields plus `migration_id`, `source_green_pointer`, `target_blue_pointer`, `temporal_workflow_id`.
    4. `PointerPromoted`: above fields plus `migration_id`, `retired_green_pointer`, `promoted_blue_pointer`, `verified_delta_lag: 0`.
    5. `ReindexAborted`: above fields plus `migration_id`, `orphaned_blue_pointer`, `abort_reason`.
    6. `KBDeleted`: above fields plus `candidate_index_pointer`, `active_migrations_cancelled`.
- **Payload Unpacking & Header Verification**:
  - Assert inclusion of all required metadata: `event_id` (valid UUIDv4), `tenant_id`, `kb_id`, `version`, `state`, `routing_policy`, `occurred_at` (RFC3339Nano UTC).
  - Assert that expanded JSON payload deserializes cleanly into structured DTOs without data loss or type coercion errors.
  - Verify header projection compatibility with Debezium SMT contract (`eventType`, `aggregateVersion`, `tenantId`).

### C. Chaos & Outbox Transaction Atomicity Tests (`test/chaos/outbox_atomicity_test.go`)

- **Transactional Rollback Atomicity**:
  - Execute entity mutations under injected failure conditions (e.g. database execution failure, context deadline cancellation midway through transaction).
  - Verify that when an entity mutation fails or aborts, zero orphaned outbox records are committed or left visible in the repository.
- **Concurrent OCC Conflict Storms Under Load**:
  - Execute 20 concurrent goroutines attempting to update the same Knowledge Base entity.
  - Assert that exactly 1 transaction succeeds, incrementing `version` by 1 and writing exactly 1 outbox event.
  - Assert that the remaining 19 operations fail deterministically with `ErrVersionConflict` (HTTP 409 / gRPC `codes.Aborted`), emitting zero duplicate outbox records.
- **Soft Delete Outbox Atomicity**:
  - Execute `DeleteKnowledgeBase` on an active knowledge base with an active reindexing migration.
  - Verify that the active migration is marked `ABORTED`, the candidate index pointer is captured in the `KBDeleted` outbox event for cleanup, and active migration gauges decrement cleanly.

### D. Load Benchmarking Suite (`test/benchmark/load_bench_test.go`)

- **Standard Go Microbenchmarks**:
  - `BenchmarkDirectCacheRead`: Measure raw key lookup throughput and memory allocations.
  - `BenchmarkBufferPoolAllocations`: Verify zero heap allocations during serialization using `sync.Pool` (`internal/infrastructure/observability/buffer_pool.go`).
  - `BenchmarkOCCConcurrentMutation`: Measure transaction execution throughput under optimistic concurrency control.
- **Synthetic Quantile Load Tests**:
  - `TestLoad_DirectCacheReadSLI_P99Sub5ms`:
    - Generate 3,000 requests across concurrent worker goroutines against versioned cache entries (`kb:{tenant_id}:{id}:v{version}`).
    - Measure the elapsed load window and require completed requests per second to meet the 3,000 RPS target.
    - Record high-resolution durations, sort latency distribution, and assert $P50 \le 1\text{ms}$, $P90 \le 3\text{ms}$, and $P99 \le 5\text{ms}$.
  - `TestLoad_OCCMutationLatency_P99Sub20ms`:
    - Execute concurrent writes across distinct tenant entities, recording transaction durations. Exercises live PostgreSQL transactions when configured or in-memory OCC persistence in standalone mode.
    - Assert write latency $P99 \le 20\text{ms}$.
  - `TestLoad_MemoryFootprintHeap_Sub128MB`:
    - Read memory stats via `runtime.ReadMemStats` before and after high-throughput batch serialization to measure heap-only memory.
    - Assert that heap in-use and allocation remain strictly below the $128\text{MiB}$ threshold (`134,217,728` bytes).

### E. Production Packaging, Helm Chart & Declarative ArgoCD GitOps Manifest (`deployments/`)

- **Multi-Stage Distroless Dockerfile (`deployments/docker/Dockerfile`)**:
  - Builder stage: `golang:1.26-alpine` (or `golang:1.26-bookworm`), statically compiled with `CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"`.
  - Final stage: `gcr.io/distroless/static:nonroot` running as non-root UID `65532:65532`.
  - Expose ports: `8080` (HTTP REST), `50051` (gRPC), `9090` (Metrics).
- **High-Availability Kubernetes Helm Chart (`deployments/helm/rag-kb-catalog-service/`)**:
  - `Chart.yaml`: Helm 3 chart metadata (`apiVersion: v2`, `name: rag-kb-catalog-service`, `version: 0.1.0`).
  - `values.yaml`: Configurable replicas (default 3), image repository/tag, resources (requests: 100m CPU / 64MiB mem, limits: 500m CPU / 128MiB mem), ports, probes, env, secrets.
  - `templates/deployment.yaml`:
    - Enforce `PSS: restricted` pod security context: `runAsNonRoot: true`, `runAsUser: 65532`, `runAsGroup: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: ["ALL"]`, `seccompProfile: { type: "RuntimeDefault" }`.
    - Resource requests (CPU 100m, memory 64MiB) and limits (CPU 500m, memory 128MiB).
    - Liveness probe: HTTP `GET /healthz` on port 8080 (`initialDelaySeconds: 5`, `periodSeconds: 10`).
    - Dual-dependency Readiness probe: HTTP `GET /ready` on port 8080 (`initialDelaySeconds: 5`, `periodSeconds: 5`).
    - Anti-affinity: `topology.kubernetes.io/zone` preferred/required scheduling across distinct availability zones.
  - `templates/service.yaml`:
    - Expose multi-port service:
      - `http`: 8080 (REST OpenAPI & Docs `/docs`, `/redoc`, `/openapi.json`)
      - `grpc`: 50051 (gRPC HTTP/2 control-plane)
      - `metrics`: 9090 (dedicated internal Prometheus scrape listener, annotated with `prometheus.io/scrape: "true"`)
  - `templates/poddisruptionbudget.yaml`: `minAvailable: 2`.
  - `templates/prometheusrule.yaml`: PrometheusRule CRD (`monitoring.coreos.com/v1`) with 4 mandatory alert rules:
    1. **HighMutationConflictRate**:
       ```promql
       sum(rate(kb_catalog_optimistic_conflicts_total[5m])) / sum(rate(kb_catalog_requests_total{method=~"Update.*|Trigger.*|Promote.*|Abort.*|Delete.*"}[5m])) > 0.05
       ```
       Duration: `5m`, severity: `warning`.
    2. **P99ReadLatencyDegradation**:
       ```promql
       histogram_quantile(0.99, sum(rate(kb_catalog_request_duration_seconds_bucket{method="GetKnowledgeBase"}[3m])) by (le)) > 0.010
       ```
       Duration: `3m`, severity: `warning`.
    3. **ReindexConvergenceStall**:
       ```promql
       kb_catalog_migration_delta_lag_records > 0 and (deriv(kb_catalog_migration_delta_lag_records[15m]) >= 0)
       ```
       Duration: `15m`, severity: `critical`.
    4. **DatabasePoolSaturation**:
       ```promql
       (pgx_pool_acquired_conns / pgx_pool_max_conns) > 0.85
       ```
       Duration: `2m`, severity: `critical`.
  - `templates/serviceaccount.yaml`, `templates/configmap.yaml`, `templates/secret.yaml`.
- **Declarative ArgoCD GitOps Application Manifest (`deployments/argocd/application.yaml`)**:
  - `apiVersion: argoproj.io/v1alpha1`, `kind: Application`.
  - `metadata.name: rag-kb-catalog-service`, `metadata.namespace: argocd`.
  - Source path: `deployments/helm/rag-kb-catalog-service` with `values.yaml`.
  - Destination namespace: `rag-system`.
  - Sync Policy: `automated: { prune: true, selfHeal: true }`.
  - Sync Options: `CreateNamespace=true`, `ApplyOutOfSyncOnly=true`, `PruneLast=true`.
  - Retry Policy: `limit: 5`, `backoff: { duration: 5s, factor: 2, maxDuration: 3m }`.
  - Pure infrastructure declaration: zero ArgoCD imports inside Go code.

### F. Build Tooling & CI/CD Updates

- **Makefile Updates (`Makefile`)**:
  - Add `bench`: Run load benchmarks (`go test -v -bench=. -run=^Benchmark ./test/benchmark/...`).
  - Add `bench-load`: Run synthetic quantile load tests (`go test -v -run=^TestLoad ./test/benchmark/...`).
  - Add `helm-lint`: Run `helm lint deployments/helm/rag-kb-catalog-service`.
- **GitHub Actions CI/CD Pipeline Updates (`.github/workflows/ci.yml`)**:
  - Add Helm chart validation step (`helm lint deployments/helm/rag-kb-catalog-service`).
  - Add Docker build verification step (`docker build -f deployments/docker/Dockerfile .`).
  - Add load benchmark execution step to test suite.

### G. Specification & Progress Updates

- Update `.ai/doc-api.md`: Document Debezium connector specification, Kafka event routing contract (`rag.catalog.events`), header schemas, and Prometheus alert matrix.
- Update `api/openapi/openapi.yaml`: Ensure complete schema synchronization.
- Update `.ai/progress-tracker.md`: Mark Phase 4 completed.

---

## 3. Verification Checklist

1. `gofmt -s -w .`, `go vet ./...`, `golangci-lint run ./...`
2. Domain layer purity: zero third-party I/O imports in `internal/domain/`.
3. Secured logging audit: zero leaked credentials/secrets in logs or manifests.
4. Continuous Doc API & OpenAPI 3.1 synchronization (`.ai/doc-api.md`, `api/openapi/openapi.yaml`).
5. Outbox CDC contract tests (`test/unit/outbox_cdc_test.go`) pass with all 6 events verified.
6. Chaos and outbox atomicity tests (`test/chaos/outbox_atomicity_test.go`) pass cleanly.
7. Load benchmark suite (`test/benchmark/load_bench_test.go`) confirms $P99 \le 5\text{ms}$ reads, $P99 \le 20\text{ms}$ writes, and RSS $\le 128\text{MiB}$.
8. Production manifests verified: `helm lint deployments/helm/rag-kb-catalog-service` passes cleanly.
9. Progress tracker (`.ai/progress-tracker.md`) updated.
