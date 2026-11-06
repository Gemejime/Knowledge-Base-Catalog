# RAG Knowledge Base Catalog Service (`rag-kb-catalog-service`)

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Architecture](https://img.shields.io/badge/Architecture-Hexagonal%20%2F%20Ports%20%26%20Adapters-orange.svg)](#architecture--hexagonal-design)
[![Security](https://img.shields.io/badge/Runtime-Distroless%20Non--Root-success.svg)](deployments/docker/Dockerfile)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-PSS%20Restricted-326CE5?logo=kubernetes&logoColor=white)](deployments/helm/rag-kb-catalog-service/)

`rag-kb-catalog-service` is the authoritative control-plane metadata microservice and system of record for Knowledge Base (KB) lifecycles, tenant isolation, strict vector schema immutability, zero-downtime Blue-Green migrations, and transactional Change Data Capture (CDC) governance within a Disaggregated Hybrid Multimodal Retrieval-Augmented Generation (RAG) architecture.

---

## Table of Contents

- [System Architecture](#system-architecture)
- [Core Architectural Invariants (ADR-001 – ADR-006)](#core-architectural-invariants-adr-001--adr-006)
- [Quantitative SLIs and SLOs](#quantitative-slis-and-slos)
- [API Documentation & Contracts](#api-documentation--contracts)
- [Folder Structure](#folder-structure)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Local Infrastructure Setup](#local-infrastructure-setup)
  - [Environment Configuration](#environment-configuration)
  - [Running the Service](#running-the-service)
- [Testing & Quality Gates](#testing--quality-gates)
- [Deployment & Secret Management](#deployment--secret-management)
- [Observability & Prometheus Alerts](#observability--prometheus-alerts)

---

## System Architecture

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
                │ Direct Read (P99 <= 5ms)    │ PgBouncer (exec mode)      │ Client Trigger
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

The service is built on a **5-Layer Pragmatic Hexagonal Architecture**:
1. **Domain Layer** (`internal/domain/`): Pure Go entities, immutable value objects, lifecycle state machine, and domain errors. **Zero third-party I/O or transport imports allowed**.
2. **Application Layer** (`internal/application/`): Command and query orchestrators handling OCC versions, validation, and outbox event staging.
3. **Infrastructure Layer** (`internal/infrastructure/`): Adapters for PostgreSQL 16 (`pgx/v5`), Redis 7.2 Cluster (`go-redis/v9`), Temporal orchestration, and Prometheus observability.
4. **Transport Layer** (`internal/transport/`): REST HTTP (`net/http` ServeMux with OpenAPI/Swagger UI) and gRPC (`google.golang.org/grpc`).
5. **Entrypoint & Wiring** (`cmd/catalog-service/`): Dependency injection, graceful shutdown, and listener lifecycle management.

---

## Core Architectural Invariants (ADR-001 – ADR-006)

- **ADR-001: Strict Vector Schema Immutability**: Structural parameters (`embedding_model`, `dimensions`, `distance_metric`) cannot be modified in-place (`ErrStructuralMutationDisallowed`). All structural upgrades execute via the tracked Blue-Green migration state machine: `ACTIVE` $\rightarrow$ `REINDEXING` $\rightarrow$ `PROMOTED` / `ABORTED`.
- **ADR-002: Dual-Write Routing & Replay Buffer Quiescence**: During `REINDEXING`, ingress routing switches to `DUAL_WRITE`. Index promotion (`PromoteIndexPointer`) is strictly blocked until backfill completes, the ingestion write fence quiesces pre-fence writes, and the server delta replay lag (`kb_catalog_migration_delta_lag_records`) equals `0`.
- **ADR-003: Versioned Redis Cluster Caching & CRC16 Co-Location**: Reads serve from Redis 7.2 Cluster using monotonic versioned keys `kb:{tenant_id}:{id}:v{version}` co-located with `kb:{tenant_id}:{id}:latest_version` using `{tenant_id}` hash tags with a 1-hour safety TTL. No volatile in-process L1 invalidation buses.
- **ADR-004: Optimistic Concurrency Control (OCC) & PgBouncer Compatibility**: `pgx/v5` runs with `default_query_exec_mode = exec` to ensure complete compatibility with PgBouncer transaction-mode pooling. Every update validates monotonic integer version checks (`version = version + 1`), returning HTTP `409 Conflict` / gRPC `codes.Aborted` on concurrent collisions.
- **ADR-005: Transactional Outbox Delegated to Debezium**: Outbox events (`KBCreated`, `KBUpdated`, `ReindexInitiated`, `PointerPromoted`, `ReindexAborted`, `KBDeleted`) are inserted atomically into the `outbox_events` table in the same transaction as entity updates. Zero internal polling loops; external Debezium CDC captures events from PostgreSQL WAL.
- **ADR-006: Hexagonal Domain Purity & Scope Boundary**: Excludes hardware inference model registries (tokenizers, GPU mappings) and corpus runtime analytics from the control-plane service, delegating them to downstream model routers and metrics collectors.

---

## Quantitative SLIs and SLOs

| Objective | Target | Implementation |
| :--- | :--- | :--- |
| **Direct Cache Read Latency** | $P99 \le 5\text{ms}$ at 3,000 RPS | Versioned Redis Cluster reads with pipelining |
| **Database Mutation Latency** | $P99 \le 20\text{ms}$ under peak concurrent writes | PostgreSQL 16 + PgBouncer `exec` mode with OCC |
| **Migration Data Loss** | Zero lost writes ($0$) | Ingestion write fence + zero-lag replay buffer check |
| **Memory Footprint (RSS)** | $\le 128\text{MiB}$ per pod | `sync.Pool` allocation reuse; zero in-process L1 cache |
| **Service Availability** | $99.99\%$ uptime | PodDisruptionBudget (`minAvailable: 2`) + Zone anti-affinity |

---

## API Documentation & Contracts

The service natively serves interactive API documentation and contracts on its HTTP listener:

- **Swagger UI**: [`http://localhost:8080/docs`](http://localhost:8080/docs)
- **Redoc UI**: [`http://localhost:8080/redoc`](http://localhost:8080/redoc)
- **OpenAPI 3.1 JSON**: [`http://localhost:8080/openapi.json`](http://localhost:8080/openapi.json) or [`http://localhost:8080/api/docs`](http://localhost:8080/api/docs)
- **Liveness Probe**: [`http://localhost:8080/healthz`](http://localhost:8080/healthz)
- **Readiness Probe**: [`http://localhost:8080/ready`](http://localhost:8080/ready) (verifies PostgreSQL pool & Redis Cluster health)
- **Prometheus Metrics**: `http://localhost:9090/metrics` (isolated metrics listener)
- **gRPC Service**: `localhost:50051` (`rag.catalog.v1.CatalogService`)

### Core REST Endpoints

| Method | Path | Summary | Auth Role |
| :--- | :--- | :--- | :--- |
| `POST` | `/v1/knowledge-bases` | Create Knowledge Base | `Owner`, `Editor` |
| `GET` | `/v1/knowledge-bases/{kb_id}` | Retrieve Knowledge Base by ID | `Owner`, `Editor`, `Viewer`, `Auditor` |
| `PATCH` | `/v1/knowledge-bases/{kb_id}` | Update Knowledge Base Metadata | `Owner`, `Editor` |
| `POST` | `/v1/knowledge-bases/{kb_id}/reindex` | Initiate Blue-Green Re-Indexing | `Owner`, `Editor` |
| `POST` | `/v1/knowledge-bases/{kb_id}/promote` | Promote Green Index Pointer | `Owner`, `Editor` |
| `POST` | `/v1/knowledge-bases/{kb_id}/abort` | Abort Migration & Quiesce Ingress | `Owner`, `Editor` |
| `DELETE` | `/v1/knowledge-bases/{kb_id}` | Soft-Delete Knowledge Base | `Owner` |
| `GET` | `/v1/knowledge-bases` | List & Filter Knowledge Bases | `Owner`, `Editor`, `Viewer`, `Auditor` |

---

## Folder Structure

```
.
├── cmd/
│   └── catalog-service/         # Microservice main binary entrypoint
├── api/
│   ├── proto/v1/                # Protobuf v3 gRPC service contracts
│   └── openapi/                 # OpenAPI 3.1 YAML & JSON specifications
├── internal/
│   ├── domain/                  # Pure Hexagonal Domain entities & state machine
│   ├── application/             # Command, Query, and DTO orchestrators
│   ├── infrastructure/          # PostgreSQL, Redis Cluster, Temporal, Observability
│   └── transport/               # HTTP REST & gRPC transport layers, auth middlewares
├── deployments/
│   ├── docker/                  # Distroless Dockerfile & local docker-compose
│   ├── helm/                    # High-Availability Helm charts (app & redis-cluster)
│   ├── argocd/                  # Declarative GitOps Application manifests
│   ├── debezium/                # Kafka Connect PostgreSQL CDC connector configs
│   └── SECRET_MANAGEMENT.md     # Production Secret Management & Rotation Runbooks
├── scripts/                     # Cluster deployment & environment management utilities
├── test/
│   ├── unit/                    # Fast domain & handler unit tests
│   ├── integration/             # Persistence & cache integration tests
│   ├── chaos/                   # Reindex failure & rollback chaos tests
│   └── benchmark/               # Quantile latency & memory footprint benchmarks
├── Makefile                     # Build, lint, and test orchestration targets
└── requirements.txt             # Python tooling dependencies
```

---

## Getting Started

### Prerequisites

- **Go**: 1.23 or newer
- **Docker & Docker Compose**: For local PostgreSQL, Redis, and Temporal dependencies
- **Helm 3**: For chart linting and template verification (optional)
- **Make**: For running lifecycle targets

### Local Infrastructure Setup

Spin up local Redis for development:

```bash
docker compose -f deployments/docker/docker-compose.redis.yml up -d
```

### Environment Configuration

Copy the documented environment template:

```bash
cp .env.example .env
```

Ensure `JWT_HMAC_SECRET` is set to a secure string of at least 32 bytes (256 bits).

### Running the Service

```bash
# Build the binary
make build

# Run the catalog service
./bin/rag-kb-catalog-service
```

The service will start:
- REST API & Docs: `http://localhost:8080`
- gRPC Server: `localhost:50051`
- Metrics: `http://localhost:9090/metrics`

---

## Testing & Quality Gates

The project enforces strict automated verification suites:

```bash
# Format code
make fmt

# Run Go vet analysis
make vet

# Run linter
make lint

# Run all unit tests
make test-unit

# Run integration tests (requires local test PostgreSQL/Redis or in-memory mode)
make test-integration

# Run migration chaos & rollback tests
make test-chaos

# Run synthetic quantile load benchmarks
make bench-load
```

---

## Deployment & Secret Management

### Helm 3 Deployment

The production Helm chart enforces Kubernetes **Pod Security Standards (Restricted)**, non-root execution (UID 65532), and strict Pod Disruption Budgets:

```bash
helm upgrade --install rag-kb-catalog-service deployments/helm/rag-kb-catalog-service \
  --namespace rag-system \
  --set-literal secrets.jwtHmacSecret="${JWT_HMAC_SECRET}" \
  --set-literal secrets.postgresPassword="${POSTGRES_PASSWORD}" \
  --set-literal secrets.redisPassword="${REDIS_PASSWORD}" \
  --set-literal secrets.databaseUrl="${DATABASE_URL}"
```

For external secret management (External Secrets Operator, HashiCorp Vault), set `secrets.manageSecret=false` to prevent Helm from rendering an unmanaged secret.

Refer to [`deployments/SECRET_MANAGEMENT.md`](deployments/SECRET_MANAGEMENT.md) for the complete Secret Rotation Runbook (including zero-downtime dual-login database credential rotation).

---

## Observability & Prometheus Alerts

Alerting rules are defined in [`deployments/helm/rag-kb-catalog-service/templates/prometheusrule.yaml`](deployments/helm/rag-kb-catalog-service/templates/prometheusrule.yaml):

1. **`HighMutationConflictRate`**: Triggered when OCC mutation conflicts exceed 5% over 5 minutes.
2. **`P99ReadLatencyDegradation`**: Triggered when `GetKnowledgeBase` P99 latency exceeds 10ms for 3 minutes (SLO: $\le 5\text{ms}$).
3. **`ReindexConvergenceStall`**: Triggered when migration delta replay backlog remains $>0$ without decreasing for 15 minutes.
4. **`DatabasePoolSaturation`**: Triggered when PostgreSQL pgxpool utilization exceeds 85% for 2 minutes.
