# Microservice Documented API ("Doc API") - RAG KB Catalog Service

> **MANDATORY INVARIANT**: This document specifies both the internal/external system contracts and the **HTTP Doc API & OpenAPI 3.1 Endpoints** exposed by `rag-kb-catalog-service`.
> **API ACCESSIBILITY REQUIREMENT**: The OpenAPI 3.1 specification (`api/openapi/openapi.yaml`) MUST be served directly via HTTP endpoints hosted on the service's HTTP server (default port `8080`):
>
> - `GET /docs` — Interactive OpenAPI 3.1 Documentation via **Swagger UI**.
> - `GET /redoc` — Interactive OpenAPI 3.1 Documentation via **ReDoc**.
> - `GET /openapi.json` & `GET /api/docs` — Machine-readable **OpenAPI 3.1 JSON/YAML Specification**.
>
> **CONTINUOUS DOC API UPDATE INVARIANT**: This file ([`.ai/doc-api.md`](.ai/doc-api.md)) and `api/openapi/openapi.yaml` MUST be updated on **every code change or addition**, no matter how small the created or modified code is.

---

## 1. Documentation, Operational Probe & Administrative REST Endpoints (OpenAPI 3.1)

The `rag-kb-catalog-service` runs an HTTP server (default port `8080`) serving OpenAPI 3.1 documentation, operational health/metrics probes, and administrative REST endpoints alongside the high-performance gRPC server (default port `50051`):

| Endpoint | Method | Response Format | Description |
| :--- | :--- | :--- | :--- |
| `/docs` | `GET` | `text/html` | Interactive Swagger UI powered by OpenAPI 3.1 |
| `/redoc` | `GET` | `text/html` | Interactive ReDoc UI documentation |
| `/api/docs` | `GET` | `application/json` | Raw OpenAPI 3.1 JSON specification schema |
| `/openapi.json` | `GET` | `application/json` | Standard machine-readable OpenAPI 3.1 JSON specification |
| `/healthz` | `GET` | `application/json` | Liveness probe returning `{"status": "healthy"}` |
| `/ready` | `GET` | `application/json` | Readiness probe verifying PostgreSQL (`pgxpool` exec mode) and Redis 7.2 Cluster availability |
| `/metrics` | `GET` | `text/plain` | Prometheus metrics scrape endpoint |
| `/v1/knowledge-bases` | `POST` | `application/json` | Create a new Knowledge Base (`CREATING` -> `ACTIVE`) with initial vector schema & taxonomy |
| `/v1/knowledge-bases` | `GET` | `application/json` | List Knowledge Bases for a tenant filtered by hierarchical taxonomy tags (`namespace:key=value`) |
| `/v1/knowledge-bases/{kb_id}` | `GET` | `application/json` | Get Knowledge Base metadata via direct Redis Cluster versioned read (`kb:{tenant_id}:{id}:v{ver}`) with DB fallback |
| `/v1/knowledge-bases/{kb_id}` | `PATCH` | `application/json` | Update mutable metadata, retrieval profile, or taxonomy tags using OCC (`version`). Rejects structural vector schema edits (`422 Unprocessable Entity`) |
| `/v1/knowledge-bases/{kb_id}` | `DELETE` | `204 No Content` | Soft-delete (`TOMBSTONED`) Knowledge Base and atomically emit `KBDeleted` purge event to outbox |
| `/v1/knowledge-bases/{kb_id}:triggerReindexing` | `POST` | `application/json` | Initiate Blue-Green schema migration (`ACTIVE` -> `REINDEXING`), set `DUAL_WRITE` policy, and trigger Temporal backfill DAG |
| `/v1/knowledge-bases/{kb_id}:promotePointer` | `POST` | `application/json` | Atomically promote Blue index to active pointer (`REINDEXING` -> `PROMOTED` -> `ACTIVE`) gated on `delta_lag == 0` |
| `/v1/knowledge-bases/{kb_id}:abortReindexing` | `POST` | `application/json` | Abort active migration (`REINDEXING` -> `ABORTED` -> `ACTIVE`), keep Green pointer, and emit Blue index cleanup event to outbox |

### OpenAPI 3.1 Base Specification (`api/openapi/openapi.yaml` & `/openapi.json`)

```json
{
  "openapi": "3.1.0",
  "info": {
    "title": "RAG Knowledge Base Catalog Service API",
    "description": "Control-plane metadata authority for Knowledge Base lifecycles, strict vector schema immutability, Blue-Green schema migrations, hierarchical taxonomy discovery, and transactional CDC outbox governance.",
    "version": "1.0.0"
  },
  "paths": {
    "/healthz": {
      "get": {
        "summary": "Liveness Probe",
        "operationId": "getLiveness",
        "responses": {
          "200": {
            "description": "Service process is alive",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "status": { "type": "string", "example": "healthy" }
                  },
                  "required": ["status"]
                }
              }
            }
          }
        }
      }
    },
    "/ready": {
      "get": {
        "summary": "Readiness Probe",
        "operationId": "getReadiness",
        "responses": {
          "200": {
            "description": "PostgreSQL pool and Redis Cluster are reachable",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "status": { "type": "string", "example": "ready" },
                    "postgres": { "type": "string", "example": "connected" },
                    "redis_cluster": { "type": "string", "example": "connected" }
                  },
                  "required": ["status", "postgres", "redis_cluster"]
                }
              }
            }
          },
          "503": {
            "description": "Database pool saturated or upstream dependency unreachable"
          }
        }
      }
    },
    "/v1/knowledge-bases": {
      "post": {
        "summary": "Create Knowledge Base",
        "operationId": "createKnowledgeBase",
        "responses": {
          "201": { "description": "Knowledge Base created" },
          "400": { "description": "Invalid taxonomy tag or unsupported model/dimension/metric" },
          "403": { "description": "Insufficient RBAC permissions" }
        }
      },
      "get": {
        "summary": "List Knowledge Bases",
        "operationId": "listKnowledgeBases",
        "responses": {
          "200": { "description": "Paginated list of tenant Knowledge Bases" }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}": {
      "get": {
        "summary": "Get Knowledge Base",
        "operationId": "getKnowledgeBase",
        "responses": {
          "200": { "description": "Knowledge Base configuration and pointer state" },
          "404": { "description": "Knowledge Base not found within tenant scope" }
        }
      },
      "patch": {
        "summary": "Update Knowledge Base Metadata",
        "operationId": "updateKnowledgeBase",
        "responses": {
          "200": { "description": "Knowledge Base updated and version incremented" },
          "409": { "description": "Optimistic concurrency version conflict" },
          "422": { "description": "ErrStructuralMutationDisallowed: In-place vector schema mutation rejected" }
        }
      },
      "delete": {
        "summary": "Delete Knowledge Base (Tombstone)",
        "operationId": "deleteKnowledgeBase",
        "responses": {
          "204": { "description": "Knowledge Base tombstoned and purge event written to outbox" },
          "409": { "description": "Optimistic concurrency version conflict" }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:triggerReindexing": {
      "post": {
        "summary": "Trigger Blue-Green Schema Re-Indexing",
        "operationId": "triggerReindexing",
        "responses": {
          "202": { "description": "Re-indexing state initiated, DUAL_WRITE enabled, Temporal backfill DAG triggered" },
          "409": { "description": "Migration already in progress or version mismatch" }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:promotePointer": {
      "post": {
        "summary": "Promote Blue Index Pointer",
        "operationId": "promoteIndexPointer",
        "responses": {
          "200": { "description": "Pointer atomically switched to Blue index and version bumped" },
          "409": { "description": "Precondition failed: delta replay buffer lag > 0 or OCC conflict" }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:abortReindexing": {
      "post": {
        "summary": "Abort Blue-Green Re-Indexing",
        "operationId": "abortReindexing",
        "responses": {
          "200": { "description": "Migration aborted, search pointer locked to Green, Blue cleanup event emitted" }
        }
      }
    }
  }
}
```

---

## 2. Protobuf v3 gRPC Contracts (`api/proto/v1/`)

### A. `api/proto/v1/catalog.proto`

```protobuf
syntax = "proto3";

package rag.catalog.v1;

option go_package = "github.com/gemejime/rag-kb-catalog-service/api/proto/v1;catalogv1";

import "google/protobuf/empty.proto";
import "api/proto/v1/types.proto";

service CatalogService {
  rpc CreateKnowledgeBase(CreateKBRequest) returns (KBResponse);
  rpc UpdateKnowledgeBase(UpdateKBRequest) returns (KBResponse);
  rpc GetKnowledgeBase(GetKBRequest) returns (KBResponse);
  rpc ListKnowledgeBases(ListKBsRequest) returns (ListKBsResponse);
  rpc DeleteKnowledgeBase(DeleteKBRequest) returns (google.protobuf.Empty);
  rpc TriggerReindexing(TriggerReindexingRequest) returns (MigrationResponse);
  rpc PromoteIndexPointer(PromotePointerRequest) returns (KBResponse);
  rpc AbortReindexing(AbortReindexingRequest) returns (google.protobuf.Empty);
}
```

### B. `api/proto/v1/types.proto`

```protobuf
syntax = "proto3";

package rag.catalog.v1;

option go_package = "github.com/gemejime/rag-kb-catalog-service/api/proto/v1;catalogv1";

import "google/protobuf/timestamp.proto";

enum LifecycleState {
  LIFECYCLE_STATE_UNSPECIFIED = 0;
  LIFECYCLE_STATE_CREATING = 1;
  LIFECYCLE_STATE_ACTIVE = 2;
  LIFECYCLE_STATE_REINDEXING = 3;
  LIFECYCLE_STATE_PROMOTED = 4;
  LIFECYCLE_STATE_ABORTED = 5;
  LIFECYCLE_STATE_TOMBSTONED = 6;
}

enum RoutingPolicy {
  ROUTING_POLICY_UNSPECIFIED = 0;
  ROUTING_POLICY_GREEN_ONLY = 1;
  ROUTING_POLICY_DUAL_WRITE = 2;
}

enum DistanceMetric {
  DISTANCE_METRIC_UNSPECIFIED = 0;
  DISTANCE_METRIC_COSINE = 1;
  DISTANCE_METRIC_DOT_PRODUCT = 2;
  DISTANCE_METRIC_L2 = 3;
}

message VectorSchema {
  string embedding_model = 1;
  uint32 dimensions = 2;
  DistanceMetric distance_metric = 3;
  string schema_version = 4;
}

message RetrievalProfile {
  uint32 default_top_k = 1;
  float dense_weight = 2;
  float sparse_weight = 3;
  uint32 reranker_candidate_pool_size = 4;
  float cross_encoder_confidence_threshold = 5;
}

message KnowledgeBase {
  string tenant_id = 1;
  string kb_id = 2;
  string name = 3;
  string description = 4;
  uint64 version = 5;
  LifecycleState state = 6;
  RoutingPolicy routing_policy = 7;
  string active_index_pointer = 8;
  string candidate_index_pointer = 9;
  VectorSchema active_schema = 10;
  VectorSchema candidate_schema = 11;
  RetrievalProfile retrieval_profile = 12;
  repeated string taxonomy_tags = 13; // Format: "namespace:key=value"
  google.protobuf.Timestamp created_at = 14;
  google.protobuf.Timestamp updated_at = 15;
}

message CreateKBRequest {
  string tenant_id = 1;
  string name = 2;
  string description = 3;
  VectorSchema vector_schema = 4;
  RetrievalProfile retrieval_profile = 5;
  repeated string taxonomy_tags = 6;
  string idempotency_key = 7;
}

message UpdateKBRequest {
  string tenant_id = 1;
  string kb_id = 2;
  uint64 expected_version = 3;
  optional string name = 4;
  optional string description = 5;
  optional RetrievalProfile retrieval_profile = 6;
  repeated string taxonomy_tags = 7;
  optional VectorSchema attempted_schema_mutation = 8; // Guarded: triggers ErrStructuralMutationDisallowed if mutated
}

message GetKBRequest {
  string tenant_id = 1;
  string kb_id = 2;
  optional uint64 min_version = 3;
}

message ListKBsRequest {
  string tenant_id = 1;
  repeated string taxonomy_filters = 2;
  uint32 page_size = 3;
  string page_token = 4;
}

message ListKBsResponse {
  repeated KnowledgeBase knowledge_bases = 1;
  string next_page_token = 2;
}

message DeleteKBRequest {
  string tenant_id = 1;
  string kb_id = 2;
  uint64 expected_version = 3;
}

message TriggerReindexingRequest {
  string tenant_id = 1;
  string kb_id = 2;
  uint64 expected_version = 3;
  VectorSchema target_blue_schema = 4;
}

message MigrationResponse {
  string tenant_id = 1;
  string kb_id = 2;
  string migration_id = 3;
  LifecycleState state = 4;
  RoutingPolicy routing_policy = 5;
  string candidate_index_pointer = 6;
  uint64 version = 7;
}

message PromotePointerRequest {
  string tenant_id = 1;
  string kb_id = 2;
  uint64 expected_version = 3;
  uint64 verified_delta_lag_records = 4; // Must equal 0 for promotion
}

message AbortReindexingRequest {
  string tenant_id = 1;
  string kb_id = 2;
  uint64 expected_version = 3;
  string reason = 4;
}

message KBResponse {
  KnowledgeBase knowledge_base = 1;
}
```

---

## 3. Domain Invariants, State Machine & Policy Contracts

### A. Vector Schema Immutability (`internal/domain/policy/immutability_guard.go`)
- **Rule (ADR-001)**: Direct in-place edits to `embedding_model`, `dimensions`, or `distance_metric` via `UpdateKnowledgeBase` are strictly prohibited and return `ErrStructuralMutationDisallowed`.
- **Static Validated Model Registry (`internal/domain/entity/model_reference.go`)**: Validates model identifiers against supported dimension sizes and allowed distance metrics before persistence.

### B. Blue-Green Migration State Machine (`internal/domain/entity/migration_state.go`)
- **Pure State Transition Matrix (`ValidateTransition(from, to LifecycleState, deltaLag uint64)`)**:
  - `CREATING` -> `ACTIVE`: Initial creation transition.
  - `ACTIVE` -> `REINDEXING`: Allowed via `TriggerReindexing`. Sets `routing_policy = DUAL_WRITE`, allocates `candidate_index_pointer` (Blue), inserts an active migration row in `kb_migrations`, emits `ReindexInitiated` to `outbox_events`, and triggers the Temporal backfill workflow in `rag-ingestion-coordinator`.
  - `REINDEXING` -> `PROMOTED`: Allowed via `PromoteIndexPointer` **ONLY IF** `delta_lag == 0` (`kb_catalog_migration_delta_lag_records == 0`, also referenced as `kb_migration_delta_lag_records`). If `delta_lag > 0`, returns `ErrDeltaLagNotZero` (HTTP `409 Conflict` / gRPC `FailedPrecondition`).
  - `REINDEXING` -> `ABORTED`: Allowed via `AbortReindexing` (or `AbortMigration` during worker failure rollback).
  - `REINDEXING` -> `ACTIVE` (Direct): **Strictly Disallowed** (`ErrInvalidStateTransition`) without executing an explicit `PromoteIndexPointer` (with `delta_lag == 0`) or `AbortReindexing` transition first.
- **Aggregate Settlement After `PROMOTED` or `ABORTED`**:
  - When `PromoteIndexPointer` transitions the migration through `PROMOTED`, the migration record in `kb_migrations` is persisted as `PROMOTED`, `active_index_pointer` atomically switches to the Blue pointer, `active_schema` updates to `candidate_schema`, `candidate_index_pointer` is cleared, `routing_policy` resets to `GREEN_ONLY`, the KB operational state settles at `ACTIVE` (`version = version + 1`), and `PointerPromoted` is written to `outbox_events`.
  - When `AbortReindexing` transitions the migration through `ABORTED`, the migration record in `kb_migrations` is persisted as `ABORTED`, `active_index_pointer` remains locked to Green, `candidate_index_pointer` is cleared, `routing_policy` resets to `GREEN_ONLY`, the KB operational state resets to `ACTIVE` (`version = version + 1`), and `ReindexAborted` (cleanup event to purge the orphaned Blue index) is written to `outbox_events`.

### C. Hierarchical Taxonomy Parser (`internal/domain/entity/taxonomy.go`)
- Enforces standardized key-value namespace syntax (`namespace:key=value` or `namespace:value`, e.g., `env:production`, `compliance:hipaa`, `domain:finance`).
- Rejects malformed tokens, empty namespaces, or unescaped control characters.

---

## 4. PostgreSQL 16 & Redis 7.2 Persistence Contracts

### A. PostgreSQL 16 Relational Schema (`internal/infrastructure/persistence/migrations/`)
- **Connection Mode**: `pgx/v5` configured with `default_query_exec_mode = exec` (`pgx.QueryExecModeExec`) for PgBouncer transaction pooling compatibility.

```sql
-- 1. Authoritative Knowledge Base Catalog Table
CREATE TABLE IF NOT EXISTS knowledge_bases (
    tenant_id                TEXT        NOT NULL,
    kb_id                    UUID        NOT NULL,
    name                     TEXT        NOT NULL,
    description              TEXT        NOT NULL DEFAULT '',
    version                  BIGINT      NOT NULL DEFAULT 1,
    state                    TEXT        NOT NULL CHECK (state IN ('CREATING', 'ACTIVE', 'REINDEXING', 'PROMOTED', 'ABORTED', 'TOMBSTONED')),
    routing_policy           TEXT        NOT NULL DEFAULT 'GREEN_ONLY' CHECK (routing_policy IN ('GREEN_ONLY', 'DUAL_WRITE')),
    active_index_pointer     TEXT        NOT NULL,
    candidate_index_pointer  TEXT        NOT NULL DEFAULT '',
    active_schema            JSONB       NOT NULL,
    candidate_schema         JSONB,
    retrieval_profile        JSONB       NOT NULL,
    taxonomy_tags            JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, kb_id),
    CONSTRAINT uq_kb_tenant_name UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS idx_kb_tenant_taxonomy ON knowledge_bases USING GIN (taxonomy_tags);
CREATE INDEX IF NOT EXISTS idx_kb_tenant_state ON knowledge_bases (tenant_id, state, kb_id);

-- 2. Blue-Green Schema Migration History & Audit Table
CREATE TABLE IF NOT EXISTS kb_migrations (
    tenant_id                TEXT        NOT NULL,
    migration_id             UUID        NOT NULL,
    kb_id                    UUID        NOT NULL,
    source_green_pointer     TEXT        NOT NULL,
    target_blue_pointer      TEXT        NOT NULL,
    source_schema            JSONB       NOT NULL,
    target_schema            JSONB       NOT NULL,
    state                    TEXT        NOT NULL CHECK (state IN ('REINDEXING', 'PROMOTED', 'ABORTED')),
    delta_lag_records        BIGINT      NOT NULL DEFAULT 0,
    temporal_workflow_id     TEXT        NOT NULL DEFAULT '',
    abort_reason             TEXT        NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, migration_id),
    FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_bases (tenant_id, kb_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_kb_migrations_active ON kb_migrations (tenant_id, kb_id, state);

-- 3. Transactional CDC Outbox Table (Tailed via PostgreSQL WAL by Debezium)
CREATE TABLE IF NOT EXISTS outbox_events (
    event_id                 UUID        NOT NULL,
    tenant_id                TEXT        NOT NULL,
    aggregate_type           TEXT        NOT NULL DEFAULT 'KnowledgeBase',
    aggregate_id             UUID        NOT NULL,
    event_type               TEXT        NOT NULL CHECK (event_type IN ('KBCreated', 'KBUpdated', 'ReindexInitiated', 'PointerPromoted', 'ReindexAborted', 'KBDeleted')),
    aggregate_version        BIGINT      NOT NULL,
    payload                  JSONB       NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_outbox_tenant_created ON outbox_events (tenant_id, created_at);

-- 4. Tenant-Scoped Idempotency Store
CREATE TABLE IF NOT EXISTS idempotency_keys (
    tenant_id                TEXT        NOT NULL,
    idempotency_key          TEXT        NOT NULL,
    operation                TEXT        NOT NULL,
    request_hash             TEXT        NOT NULL,
    response_payload         JSONB       NOT NULL,
    status_code              INT         NOT NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at               TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, idempotency_key)
);
```

- **Optimistic Concurrency Control (OCC) Update Pattern (ADR-004)**:
  ```sql
  UPDATE knowledge_bases
  SET name = $4,
      description = $5,
      retrieval_profile = $6,
      taxonomy_tags = $7,
      version = version + 1,
      updated_at = NOW()
  WHERE tenant_id = $1
    AND kb_id = $2
    AND version = $3
    AND state != 'TOMBSTONED'
  RETURNING version, updated_at;
  ```
  If `RowsAffected() == 0`, the repository returns `ErrVersionConflict` (mapped to HTTP `409 Conflict` / gRPC `codes.Aborted` and incrementing `kb_catalog_optimistic_conflicts_total`).

### B. Redis 7.2 Cluster Versioned Key Contract (ADR-003)
- **Key Format & CRC16 Hash-Tag Co-Location**:
  - Versioned payload key: `kb:{tenant_id}:{kb_id}:v{version}`
  - Monotonic version pointer key: `kb:{tenant_id}:{kb_id}:latest_version`
  - Because Redis 7.2 Cluster hashes the first `{...}` substring (`{tenant_id}`), both keys for a given tenant automatically map to the **exact same Redis Cluster CRC16 hash slot**, enabling atomic single-RTT Lua (`EVALSHA`) or pipelined operations without `CROSSSLOT` errors.
- **TTL**: Bounded safety TTL (default `3600s` / 1 hour).
- **Coherence & Monotonic Write-Through**:
  - Zero in-process L1 caching and zero Pub/Sub invalidation buses.
  - Upon committing version `v+1` in PostgreSQL, the service writes `kb:{tenant_id}:{kb_id}:v{v+1}` to Redis Cluster and advances `kb:{tenant_id}:{kb_id}:latest_version` only if `v+1 > current_latest_version`, guaranteeing that concurrent readers never observe stale data even under out-of-order network scheduling.
  - On `GetKnowledgeBase`, if the caller specifies `min_version` (or reads the latest version), a single-RTT Lua script resolves the versioned key `kb:{tenant_id}:{kb_id}:v{version}`; on cache miss (or if cached version `< min_version`), the handler falls back to PostgreSQL and populates the versioned Redis key.

---

## 5. Prometheus Observability & Alerting Matrix

| Metric Identifier | Type | Labels | Description |
| :--- | :--- | :--- | :--- |
| `kb_catalog_requests_total` | Counter | `method`, `tenant`, `status_code` | Total gRPC and REST requests partitioned by method, tenant, and status code |
| `kb_catalog_request_duration_seconds` | Histogram | `method`, `protocol` | Handler execution latencies ($P50, P90, P99$) |
| `kb_catalog_optimistic_conflicts_total` | Counter | `tenant`, `operation` | OCC version mismatch events (HTTP 409 / gRPC Aborted) |
| `kb_catalog_migration_delta_lag_records` | Gauge | `tenant`, `kb_id` | Outstanding delta write backlog before Blue-Green cutover (aliased as `kb_migration_delta_lag_records` in Runbook 1) |
| `kb_catalog_active_migrations` | Gauge | `tenant` | Currently active Blue-Green migrations in `REINDEXING` state |

### Production Alert Rules
1. **High Mutation Conflict Rate**: Triggers if optimistic conflicts (`kb_catalog_optimistic_conflicts_total`) exceed `5%` of write traffic over a 5-minute period.
2. **P99 Read Latency Degradation**: Triggers if Redis direct read $P99$ latency exceeds `10ms` for `> 3 minutes`.
3. **Re-Index Convergence Stall**: Triggers if an active migration remains in `REINDEXING` with delta buffer lag $> 0$ without decrease for `> 15 minutes`.
4. **Database Pool Saturation**: Triggers if active PgBouncer connections exceed `85%` of pool capacity for `> 2 minutes`.
