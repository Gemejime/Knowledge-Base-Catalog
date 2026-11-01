# RAG Knowledge Base Catalog Service - Project Overview

## 1. System Summary

The **`rag-kb-catalog-service`** serves as the central control-plane metadata authority and system of record for Knowledge Base (KB) lifecycles, tenant isolation, and retrieval configurations within the Disaggregated Hybrid Multimodal RAG architecture. Implemented as a high-concurrency Go (Golang 1.23+) vertical domain microservice, it enforces strict vector schema immutability, orchestrates zero-downtime Blue-Green schema migrations, manages hierarchical taxonomy metadata, and emits transactional state mutations via Change Data Capture (CDC) outbox events.

```
┌──────────────────────────────────────────────────────────────────────────────────────────┐
│                               Edge API Gateway / Ingress                                 │
└─────────────────────────────┬────────────────────────────────────────────────────────────┘
                              │ gRPC / REST (CRUD & Discovery)
                              ▼
┌──────────────────────────────────────────────────────────────────────────────────────────┐
│                                rag-kb-catalog-service                                    │
│  - Enforces Vector Schema Immutability (ErrStructuralMutationDisallowed)                 │
│  - Executes Optimistic Concurrency Control (version = version + 1)                       │
│  - Maintains Blue-Green State Machine & Dual-Write Ingestion Routing Rules               │
└───────────────┬─────────────────────────────┬────────────────────────────┬───────────────┘
                │ Direct Read (Sub-5ms)       │ PgBouncer (exec mode)      │ Client Trigger
                ▼                             ▼                            ▼
┌──────────────────────────┐    ┌───────────────────────────┐    ┌───────────────────────────┐
│     Redis 7.2 Cluster    │    │     PostgreSQL 16 DB      │    │     Temporal Cluster      │
│ - Versioned Keys:        │    │ - KB Catalog & State      │    │ - Triggers Backfill DAGs  │
│   kb:{tenant}:{id}:v{v}  │    │ - Transactional Outbox    │    │   in Ingestion Coord      │
└──────────────────────────┘    └──────────────┬────────────┘    └───────────────────────────┘
                                               │ WAL Tailing (CDC)
                                               ▼
                                ┌───────────────────────────┐
                                │    Debezium Connector     │
                                │ - Streams Outbox Events   │
                                │   to Apache Kafka         │
                                └───────────────────────────┘
```

---

## 2. Strategic Objectives

1. **Authoritative Control-Plane Metadata Governance**: Serve as the definitive system of record for Knowledge Base lifecycle states (`CREATING`, `ACTIVE`, `REINDEXING`, `PROMOTED`, `ABORTED`, `TOMBSTONED`), tenant isolation barriers, and retrieval profile configurations.
2. **Zero-Downtime Blue-Green Schema Migration**: Drive safe vector schema upgrades (embedding models, dimension changes, distance metrics) via an orchestrated state machine enforcing delta write replay (`DUAL_WRITE`) and atomic pointer promotions gated on zero replay lag.
3. **Strict Vector Schema Immutability**: Enforce immutability on physical index parameters (`ErrStructuralMutationDisallowed`), guaranteeing that incompatible dimensional or metric modifications cannot be applied directly in-place.
4. **Transactional CDC Event Sourcing**: Record all state and configuration mutations (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) within atomic PostgreSQL database transactions via a dedicated outbox table for external WAL-driven Debezium/Kafka CDC streaming.
5. **Taxonomy & Metadata Discovery**: Provide low-latency querying of tenant corpora filtered by standardized hierarchical namespaces (e.g., `env:production`, `compliance:hipaa`, `domain:finance`).
6. **Continuous Documentation & Contract Synchronization**: Ensure OpenAPI 3.1 specifications (`api/openapi/openapi.yaml`, `/docs`, `/redoc`, `/api/docs`, `/openapi.json`), Protobuf v3 contracts (`api/proto/v1/catalog.proto`, `api/proto/v1/types.proto`), and `.ai/doc-api.md` are continuously synchronized with every code modification.

---

## 3. Quantitative Success Criteria (SLIs & SLOs)

- **Direct-Cache Read Latency**: $P99 \le 5\text{ms}$ at 3,000 RPS per replica when reading active configurations directly from Redis 7.2 Cluster.
- **Database Mutation Latency**: $P99 \le 20\text{ms}$ transaction write latency under peak concurrent tenant writes via PostgreSQL 16 + PgBouncer (`default_query_exec_mode = exec`).
- **Zero Migration Data Loss**: `0` lost writes during schema re-indexing; a shared ingestion write fence (quiesce-and-drain high-water mark) ensures all pre-fence writes are drained into Blue and the server's authoritative delta replay buffer lag (`kb_catalog_migration_delta_lag_records`) hits `0` before index promotion (`PromoteIndexPointer`) occurs.
- **Cache Coherence & Eventual Consistency Model**: Immutable version-keyed Redis entries (`kb:{tenant_id}:{id}:v{version}`) co-located with `kb:{tenant_id}:{id}:latest_version` via `{tenant_id}` CRC16 hash tags and bounded safety TTLs (1 hour / `3600s`) eliminate volatile in-process L1 invalidation buses. Because PostgreSQL commits precede post-commit Redis updates, unconstrained `latest_version` reads provide **bounded eventual consistency**—a failed Redis update after a PostgreSQL commit can leave `latest_version` and the cached `active_index_pointer` stale until reconciled or TTL expiration—while callers passing `min_version` obtain causal read-your-writes freshness via PostgreSQL fallback when `cached_version < min_version`.
- **Memory Footprint**: Resident Set Size (RSS) $\le 128\text{MiB}$ per Go pod (Requests: `64MiB`, Limits: `128MiB`; CPU Requests: `100m`, Limits: `500m`) under maximum production concurrency using standard `sync.Pool` allocations.
- **Service Availability**: `99.99%` uptime with automated zero-downtime rolling updates (`PodDisruptionBudget` with `minAvailable: 2` and cross-AZ anti-affinity).

---

## 4. Scope and Exclusions

### In-Scope
- **Knowledge Base Entity Lifecycle**: Administrative state transitions (`CREATING`, `ACTIVE`, `REINDEXING`, `PROMOTED`, `ABORTED`, `TOMBSTONED`).
- **Schema Immutability & Re-Index Handshake**: Validation rules rejecting in-place edits to structural properties (dimensions, distance metrics, embedding models) with `ErrStructuralMutationDisallowed` and initiating the Blue-Green migration state machine.
- **Dual-Write / Delta Routing Policies**: Maintaining ingestion routing metadata (`GREEN_ONLY` vs. `DUAL_WRITE`) so live ingestion pipelines know whether to write to Green exclusively or dual-write to Green + Blue delta replay buffers.
- **Static Validated Model Registry**: Declarative, schema-validated mapping of supported embedding model names, valid dimension sizes, and allowed distance functions (`COSINE`, `DOT_PRODUCT`, `L2`).
- **Hierarchical Taxonomy Engine**: CRUD and filtering support for structured key-value tags (`namespace:key=value`, e.g., `env:production`, `compliance:hipaa`).
- **Coarse Access Control (RBAC/ABAC)**: Entity-level administrative permission evaluation (`Owner`, `Editor`, `Viewer`, `Auditor`).
- **Transactional Outbox Commits**: Atomically committing domain events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) alongside entity state updates in PostgreSQL.
- **Transport & Doc API**: High-performance gRPC (`catalog.proto`) for internal control-plane mesh (`rag-search-orchestrator`, `rag-ingestion-coordinator`) and OpenAPI 3.1 REST gateway (`/v1/knowledge-bases`, `/docs`, `/redoc`, `/api/docs`, `/openapi.json`, `/healthz`, `/ready`, `/metrics`) for platform administration.

### Exclusions (Out-of-Scope)
- **Dynamic Polyglot Hardware Model Registry**: Tokenizer execution, VLM hardware constraints, GPU memory allocation, and inference cost tiers (delegated to LLM Gateways and Inference Serving Pools).
- **Corpus Telemetry Aggregation**: Calculating uncompressed byte footprints, token totals, index fragmentation ratios, and tombstone counts (delegated to Prometheus collectors and ClickHouse).
- **Backfill Execution & Data Movement**: Chunk parsing, embedding generation, and backfill indexing (orchestrated by `rag-ingestion-coordinator` and Python compute pools).
- **Fine-Grained Document ACL Bitmaps**: Evaluating real-time Roaring Bitmaps during retrieval (managed strictly within `rag-search-orchestrator`).
- **Internal Outbox Polling**: Application-level polling loops or PostgreSQL `LISTEN/NOTIFY` processing (delegated exclusively to an external Debezium/Kafka Connect CDC pipeline tailing the PostgreSQL WAL).
