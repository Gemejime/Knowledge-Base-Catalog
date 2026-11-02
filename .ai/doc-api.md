# Microservice Documented API ("Doc API") - RAG KB Catalog Service

> **MANDATORY INVARIANT**: This document specifies both the internal/external system contracts and the **HTTP Doc API & OpenAPI 3.1 Endpoints** exposed by `rag-kb-catalog-service`.
> **API ACCESSIBILITY REQUIREMENT**: The OpenAPI 3.1 specification (`api/openapi/openapi.yaml`) MUST be served directly via HTTP endpoints hosted on the service's HTTP server (default port `8080`):
>
> - `GET /docs` — Interactive OpenAPI 3.1 Documentation via **Swagger UI**.
> - `GET /redoc` — Interactive OpenAPI 3.1 Documentation via **ReDoc**.
> - `GET /openapi.json` & `GET /api/docs` — Machine-readable **OpenAPI 3.1 JSON/YAML Specification**.
>
> **CONTINUOUS DOC API UPDATE INVARIANT**: This file ([`doc-api.md`](doc-api.md)) and `api/openapi/openapi.yaml` MUST be updated whenever any API, RPC, Protobuf message, domain error mapping, query parameter, or persistence/cache schema is created or modified—no matter how small the created or modified code is.

---

## 1. Documentation, Operational Probe & Administrative REST Endpoints (OpenAPI 3.1)

The `rag-kb-catalog-service` runs an HTTP server (default port `8080`) serving OpenAPI 3.1 documentation, operational health probes, and administrative REST endpoints alongside the high-performance gRPC server (default port `50051`) and an isolated internal metrics listener (default port `9090` via `METRICS_ADDR`). All administrative `/v1/...` endpoints require Bearer JWT authentication (`bearerAuth`), and `tenant_id` is derived strictly from the verified caller JWT context (`sub` mapped to `user_id`, `tenant_id`, and `roles`), never trusted from unverified client request payloads:

| Endpoint | Method | Response Format | Description |
| :--- | :--- | :--- | :--- |
| `/docs` | `GET` | `text/html` | Interactive Swagger UI powered by OpenAPI 3.1 |
| `/redoc` | `GET` | `text/html` | Interactive ReDoc UI documentation |
| `/api/docs` | `GET` | `application/json` | Raw OpenAPI 3.1 JSON specification schema |
| `/openapi.json` | `GET` | `application/json` | Standard machine-readable OpenAPI 3.1 JSON specification |
| `/healthz` | `GET` | `application/json` | Liveness probe returning `{"status": "healthy"}` |
| `/ready` | `GET` | `application/json` | Readiness probe verifying PostgreSQL (`pgxpool` exec mode) and Redis 7.2 Cluster availability |
| `/metrics` (internal `:9090`) | `GET` | `text/plain` | Prometheus metrics scrape endpoint on isolated internal listener |
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
    "description": "Control-plane metadata authority for Knowledge Base lifecycles, strict vector schema immutability, Blue-Green schema migrations, hierarchical taxonomy discovery, and transactional CDC outbox governance. Tenant scope (tenant_id) is derived strictly from verified caller JWT claims propagated by the Edge API Gateway, never trusted from unverified payload fields.",
    "version": "1.0.0"
  },
  "security": [
    { "bearerAuth": [] }
  ],
  "paths": {
    "/healthz": {
      "get": {
        "summary": "Liveness Probe",
        "operationId": "getLiveness",
        "security": [],
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
        "security": [],
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
            "description": "Database pool saturated or upstream dependency unreachable",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      }
    },
    "/v1/knowledge-bases": {
      "post": {
        "summary": "Create Knowledge Base",
        "operationId": "createKnowledgeBase",
        "security": [{ "bearerAuth": [] }],
        "parameters": [
          {
            "name": "Idempotency-Key",
            "in": "header",
            "required": false,
            "schema": { "type": "string" }
          }
        ],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/CreateKBRequest" }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Knowledge Base created",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/KBResponse" }
              }
            }
          },
          "400": {
            "description": "Invalid taxonomy tag or unsupported model/dimension/metric",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "403": {
            "description": "Insufficient RBAC permissions",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Duplicate Knowledge Base name within tenant or Idempotency-Key payload hash conflict",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      },
      "get": {
        "summary": "List Knowledge Bases",
        "operationId": "listKnowledgeBases",
        "security": [{ "bearerAuth": [] }],
        "parameters": [
          {
            "name": "taxonomy_filter",
            "in": "query",
            "required": false,
            "schema": { "type": "array", "items": { "type": "string" } }
          },
          {
            "name": "page_size",
            "in": "query",
            "required": false,
            "schema": { "type": "integer", "minimum": 1, "maximum": 100, "default": 20 }
          },
          {
            "name": "page_token",
            "in": "query",
            "required": false,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Paginated list of tenant Knowledge Bases",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ListKBsResponse" }
              }
            }
          }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}": {
      "parameters": [
        { "$ref": "#/components/parameters/KBIdPathParam" }
      ],
      "get": {
        "summary": "Get Knowledge Base",
        "operationId": "getKnowledgeBase",
        "security": [{ "bearerAuth": [] }],
        "parameters": [
          {
            "name": "min_version",
            "in": "query",
            "required": false,
            "schema": { "type": "integer", "minimum": 1 }
          }
        ],
        "responses": {
          "200": {
            "description": "Knowledge Base configuration and pointer state",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/KBResponse" }
              }
            }
          },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      },
      "patch": {
        "summary": "Update Knowledge Base Metadata",
        "operationId": "updateKnowledgeBase",
        "security": [{ "bearerAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/UpdateKBRequest" }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Knowledge Base updated and version incremented",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/KBResponse" }
              }
            }
          },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Optimistic concurrency version conflict",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "422": {
            "description": "ErrStructuralMutationDisallowed: In-place vector schema mutation rejected",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      },
      "delete": {
        "summary": "Delete Knowledge Base (Tombstone)",
        "operationId": "deleteKnowledgeBase",
        "security": [{ "bearerAuth": [] }],
        "parameters": [
          {
            "name": "expected_version",
            "in": "query",
            "required": true,
            "schema": { "type": "integer", "minimum": 1 }
          }
        ],
        "responses": {
          "204": { "description": "Knowledge Base tombstoned and purge event written to outbox" },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Optimistic concurrency version conflict",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:triggerReindexing": {
      "parameters": [
        { "$ref": "#/components/parameters/KBIdPathParam" }
      ],
      "post": {
        "summary": "Trigger Blue-Green Schema Re-Indexing",
        "operationId": "triggerReindexing",
        "security": [{ "bearerAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/TriggerReindexingRequest" }
            }
          }
        },
        "responses": {
          "202": {
            "description": "Re-indexing state initiated, DUAL_WRITE enabled, Temporal backfill DAG triggered",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/MigrationResponse" }
              }
            }
          },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Migration already in progress or version mismatch",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:promotePointer": {
      "parameters": [
        { "$ref": "#/components/parameters/KBIdPathParam" }
      ],
      "post": {
        "summary": "Promote Blue Index Pointer",
        "operationId": "promoteIndexPointer",
        "security": [{ "bearerAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/PromotePointerRequest" }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Pointer atomically switched to Blue index and version bumped",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/KBResponse" }
              }
            }
          },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Precondition failed: authoritative delta replay buffer lag > 0, write fence not drained, or OCC conflict",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      }
    },
    "/v1/knowledge-bases/{kb_id}:abortReindexing": {
      "parameters": [
        { "$ref": "#/components/parameters/KBIdPathParam" }
      ],
      "post": {
        "summary": "Abort Blue-Green Re-Indexing",
        "operationId": "abortReindexing",
        "security": [{ "bearerAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/AbortReindexingRequest" }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Migration aborted, search pointer locked to Green, Blue cleanup event emitted",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/KBResponse" }
              }
            }
          },
          "404": {
            "description": "Knowledge Base not found within tenant scope",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          },
          "409": {
            "description": "Migration not in REINDEXING state or version mismatch",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ErrorResponse" }
              }
            }
          }
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearerAuth": {
        "type": "http",
        "scheme": "bearer",
        "bearerFormat": "JWT",
        "description": "Verified JWT bearer token. Edge Gateway extracts tenant_id, sub (mapped to canonical user_id), and roles into verified request context."
      }
    },
    "responses": {
      "Unauthorized": {
        "description": "Missing or invalid authentication credentials",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/ErrorResponse" }
          }
        }
      },
      "Forbidden": {
        "description": "Insufficient RBAC permissions or tenant mismatch",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/ErrorResponse" }
          }
        }
      }
    },
    "parameters": {
      "KBIdPathParam": {
        "name": "kb_id",
        "in": "path",
        "required": true,
        "description": "UUIDv4 identifier of the Knowledge Base within the authenticated tenant scope",
        "schema": { "type": "string", "format": "uuid" }
      }
    },
    "schemas": {
      "LifecycleState": {
        "type": "string",
        "enum": ["CREATING", "ACTIVE", "REINDEXING", "PROMOTED", "ABORTED", "TOMBSTONED"]
      },
      "RoutingPolicy": {
        "type": "string",
        "enum": ["GREEN_ONLY", "DUAL_WRITE"]
      },
      "DistanceMetric": {
        "type": "string",
        "enum": ["COSINE", "DOT_PRODUCT", "L2"]
      },
      "VectorSchema": {
        "type": "object",
        "properties": {
          "embedding_model": { "type": "string" },
          "dimensions": { "type": "integer", "minimum": 1 },
          "distance_metric": { "$ref": "#/components/schemas/DistanceMetric" },
          "schema_version": { "type": "string" }
        },
        "required": ["embedding_model", "dimensions", "distance_metric", "schema_version"]
      },
      "RetrievalProfile": {
        "type": "object",
        "properties": {
          "default_top_k": { "type": "integer", "minimum": 1, "maximum": 200 },
          "dense_weight": { "type": "number", "format": "float" },
          "sparse_weight": { "type": "number", "format": "float" },
          "reranker_candidate_pool_size": { "type": "integer", "minimum": 1 },
          "cross_encoder_confidence_threshold": { "type": "number", "format": "float" }
        },
        "required": [
          "default_top_k",
          "dense_weight",
          "sparse_weight",
          "reranker_candidate_pool_size",
          "cross_encoder_confidence_threshold"
        ]
      },
      "KnowledgeBase": {
        "type": "object",
        "properties": {
          "tenant_id": { "type": "string", "readOnly": true },
          "kb_id": { "type": "string", "format": "uuid" },
          "name": { "type": "string" },
          "description": { "type": "string" },
          "version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" },
          "state": { "$ref": "#/components/schemas/LifecycleState" },
          "routing_policy": { "$ref": "#/components/schemas/RoutingPolicy" },
          "active_index_pointer": { "type": "string" },
          "candidate_index_pointer": { "type": "string" },
          "active_schema": { "$ref": "#/components/schemas/VectorSchema" },
          "candidate_schema": { "$ref": "#/components/schemas/VectorSchema" },
          "retrieval_profile": { "$ref": "#/components/schemas/RetrievalProfile" },
          "taxonomy_tags": { "type": "array", "items": { "type": "string" } },
          "created_at": { "type": "string", "format": "date-time" },
          "updated_at": { "type": "string", "format": "date-time" }
        },
        "required": [
          "tenant_id",
          "kb_id",
          "name",
          "version",
          "state",
          "routing_policy",
          "active_index_pointer",
          "active_schema",
          "retrieval_profile",
          "taxonomy_tags",
          "created_at",
          "updated_at"
        ]
      },
      "KBResponse": {
        "type": "object",
        "properties": {
          "knowledge_base": { "$ref": "#/components/schemas/KnowledgeBase" }
        },
        "required": ["knowledge_base"]
      },
      "CreateKBRequest": {
        "type": "object",
        "properties": {
          "name": { "type": "string" },
          "description": { "type": "string" },
          "vector_schema": { "$ref": "#/components/schemas/VectorSchema" },
          "retrieval_profile": { "$ref": "#/components/schemas/RetrievalProfile" },
          "taxonomy_tags": { "type": "array", "items": { "type": "string" } },
          "idempotency_key": { "type": "string" }
        },
        "required": ["name", "vector_schema", "retrieval_profile"]
      },
      "UpdateKBRequest": {
        "type": "object",
        "description": "Partial metadata update guarded by expected_version. Use update_mask to distinguish omitted taxonomy_tags (preserves existing tags) from an explicit empty array [] (clears all tags).",
        "properties": {
          "expected_version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" },
          "name": { "type": "string" },
          "description": { "type": "string" },
          "retrieval_profile": { "$ref": "#/components/schemas/RetrievalProfile" },
          "taxonomy_tags": { "type": "array", "items": { "type": "string" } },
          "update_mask": {
            "type": "array",
            "items": { "type": "string" },
            "description": "Explicit field mask (e.g., [\"name\", \"description\", \"retrieval_profile\", \"taxonomy_tags\"])."
          },
          "attempted_schema_mutation": { "$ref": "#/components/schemas/VectorSchema" }
        },
        "required": ["expected_version"]
      },
      "ListKBsResponse": {
        "type": "object",
        "properties": {
          "knowledge_bases": {
            "type": "array",
            "items": { "$ref": "#/components/schemas/KnowledgeBase" }
          },
          "next_page_token": { "type": "string" }
        },
        "required": ["knowledge_bases"]
      },
      "TriggerReindexingRequest": {
        "type": "object",
        "properties": {
          "expected_version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" },
          "target_blue_schema": { "$ref": "#/components/schemas/VectorSchema" }
        },
        "required": ["expected_version", "target_blue_schema"]
      },
      "MigrationResponse": {
        "type": "object",
        "properties": {
          "tenant_id": { "type": "string" },
          "kb_id": { "type": "string", "format": "uuid" },
          "migration_id": { "type": "string", "format": "uuid" },
          "state": { "$ref": "#/components/schemas/LifecycleState" },
          "routing_policy": { "$ref": "#/components/schemas/RoutingPolicy" },
          "candidate_index_pointer": { "type": "string" },
          "version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" }
        },
        "required": [
          "tenant_id",
          "kb_id",
          "migration_id",
          "state",
          "routing_policy",
          "candidate_index_pointer",
          "version"
        ]
      },
      "PromotePointerRequest": {
        "type": "object",
        "properties": {
          "expected_version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" },
          "verified_delta_lag_records": {
            "type": "string",
            "format": "uint64",
            "pattern": "^[0-9]+$",
            "description": "Optional client-observed delta lag assertion checked against the server's authoritative migration lag (kb_migrations.delta_lag_records)."
          }
        },
        "required": ["expected_version"]
      },
      "AbortReindexingRequest": {
        "type": "object",
        "properties": {
          "expected_version": { "type": "string", "format": "uint64", "pattern": "^[1-9][0-9]*$" },
          "reason": { "type": "string" }
        },
        "required": ["expected_version", "reason"]
      },
      "ErrorResponse": {
        "type": "object",
        "properties": {
          "code": { "type": "string" },
          "message": { "type": "string" },
          "request_id": { "type": "string" }
        },
        "required": ["code", "message"]
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
  rpc AbortReindexing(AbortReindexingRequest) returns (KBResponse);
}
```

### B. `api/proto/v1/types.proto`

```protobuf
syntax = "proto3";

package rag.catalog.v1;

option go_package = "github.com/gemejime/rag-kb-catalog-service/api/proto/v1;catalogv1";

import "google/protobuf/field_mask.proto";
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
  google.protobuf.FieldMask update_mask = 9; // Selected-field semantics: include "taxonomy_tags" with empty taxonomy_tags to clear tags; omit "taxonomy_tags" from mask to preserve existing tags
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
  optional uint64 verified_delta_lag_records = 4; // Optional client assertion checked against the server's authoritative migration lag (kb_migrations.delta_lag_records), not the source of the promotion gate
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
- **Pure State Transition Matrix (`ValidateTransition(from, to LifecycleState, authoritativeDeltaLag uint64)`)**:
  - `CREATING` -> `ACTIVE`: Initial creation transition.
  - `ACTIVE` -> `REINDEXING`: Allowed via `TriggerReindexing`. Sets `routing_policy = DUAL_WRITE`, allocates `candidate_index_pointer` (Blue), inserts an active migration row in `kb_migrations`, emits `ReindexInitiated` to `outbox_events`, and triggers the Temporal backfill workflow in `rag-ingestion-coordinator`.
  - `REINDEXING` -> `PROMOTED`: Allowed via `PromoteIndexPointer` **ONLY IF** the **server's authoritative delta lag** (`kb_migrations.delta_lag_records` / ingestion write-fence watermark, exported via `kb_catalog_migration_delta_lag_records`) equals `0` after the shared ingestion write fence has quiesced and drained all pre-fence writes into Blue (and any optional client-supplied `verified_delta_lag_records` assertion also matches `0`). If authoritative `delta_lag > 0` or the write fence is not drained, returns `ErrDeltaLagNotZero` (HTTP `409 Conflict` / gRPC `FailedPrecondition`).
  - `REINDEXING` -> `ABORTED`: Allowed via `AbortReindexing` (or `AbortMigration` during worker failure rollback).
  - `REINDEXING` -> `ACTIVE` (Direct): **Strictly Disallowed** (`ErrInvalidStateTransition`) without executing an explicit `PromoteIndexPointer` (with authoritative `delta_lag == 0`) or `AbortReindexing` transition first.
- **Aggregate Settlement After `PROMOTED` or `ABORTED`**:
  - When `PromoteIndexPointer` transitions the migration through `PROMOTED`, the migration record in `kb_migrations` is persisted as `PROMOTED`, `active_index_pointer` atomically switches to the Blue pointer, `active_schema` updates to `candidate_schema`, `candidate_index_pointer` is cleared, `routing_policy` resets to `GREEN_ONLY`, the KB operational state settles at `ACTIVE` (`version = version + 1`), and `PointerPromoted` is written to `outbox_events`.
  - When `AbortReindexing` transitions the migration through `ABORTED`, the migration record in `kb_migrations` is persisted as `ABORTED`, `active_index_pointer` remains locked to Green, `candidate_index_pointer` is cleared, `routing_policy` resets to `GREEN_ONLY`, the KB operational state resets to `ACTIVE` (`version = version + 1`), and `ReindexAborted` (cleanup event to purge the orphaned Blue index) is written to `outbox_events`.
- **Authoritative Progress Reconciliation (`UpdateMigrationProgress`)**:
  - Ingestion workers and coordinator services report authoritative backfill milestones and replay lag to PostgreSQL `kb_migrations` via `UpdateMigrationProgress(ctx, tenantID, kbID, deltaLag, backfillCompleted, writeFenceDrained)`.
  - Sets `delta_lag_records`, `backfill_completed`, and `write_fence_drained` atomically while publishing the updated lag to the Prometheus gauge `kb_catalog_migration_delta_lag_records`.
- **Temporal Backfill Orchestration (`internal/infrastructure/temporal/client.go`)**:
  - Implements `workflow.IngestionTrigger` wrapping `go.temporal.io/sdk/client`.
  - Dispatches asynchronous backfill DAGs to `rag-ingestion-coordinator` with exponential retry policies.
  - Fail-closed error handling: if errors are returned during workflow dispatch via `TriggerBackfillDAG`, `TriggerReindexing` rolls back the transition via `AbortReindexing`, retaining Green pointer stability. Failures occurring later in Temporal worker execution do not automatically trigger synchronous catalog rollback; worker failures remain observable via workflow telemetry and manual or automated reconciliation.

### C. Hierarchical Taxonomy Parser (`internal/domain/entity/taxonomy.go`)
- Enforces standardized key-value namespace syntax (`namespace:key=value` or `namespace:value`, e.g., `env:production`, `compliance:hipaa`, `domain:finance`).
- Rejects malformed tokens, empty namespaces, or unescaped control characters.

### D. Coarse RBAC Permission Matrix (`internal/domain/policy/access_evaluator.go`)
- **Read Operations (`GetKnowledgeBase`, `ListKnowledgeBases`)**: Allowed for `Owner`, `Editor`, `Viewer`, `Auditor`.
- **Standard Mutation Operations (`CreateKnowledgeBase`, `UpdateKnowledgeBase`, `TriggerReindexing`)**: Allowed for `Owner`, `Editor`.
- **Cutover & Destructive Operations (`PromoteIndexPointer`, `AbortReindexing`, `DeleteKnowledgeBase`)**: Allowed for `Owner`, `Editor` (`Viewer` and `Auditor` rejected with `ErrInsufficientPermissions` -> HTTP `403 Forbidden` / gRPC `codes.PermissionDenied`).
- **Tenant Spoofing Defense**: `tenant_id` is extracted exclusively from verified JWT/context claims (`sub` mapped to canonical `user_id`). If a gRPC request specifies a non-empty `req.GetTenantId()` that differs from the verified context `tenant_id`, the interceptor/server rejects the request with `ErrTenantMismatch` (`codes.PermissionDenied`).

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
CREATE UNIQUE INDEX IF NOT EXISTS uq_kb_migrations_one_active ON kb_migrations (tenant_id, kb_id) WHERE state = 'REINDEXING';

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

- **Optimistic Concurrency Control (OCC) Update Pattern & Disambiguation (ADR-004)**:
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
  To prevent `404` or invalid-state errors from falsely inflating `kb_catalog_optimistic_conflicts_total` (Production Alert Rule 1), repository mutations load or disambiguate the target `(tenant_id, kb_id)` within the transaction:
  - Missing or `TOMBSTONED` row -> `ErrKnowledgeBaseNotFound` (HTTP `404 Not Found` / gRPC `codes.NotFound`).
  - Disallowed lifecycle state transition -> `ErrInvalidStateTransition` (HTTP `409 Conflict` / gRPC `codes.FailedPrecondition`).
  - Existing active row where `actual_version != expected_version` (or `RowsAffected() == 0` on concurrent update race) -> `ErrVersionConflict` (HTTP `409 Conflict` / gRPC `codes.Aborted`), which exclusively increments `kb_catalog_optimistic_conflicts_total`.

### B. Redis 7.2 Cluster Versioned Key Contract (ADR-003)
- **Key Format & CRC16 Hash-Tag Co-Location**:
  - Versioned payload key: `kb:{tenant_id}:{kb_id}:v{version}`
  - Monotonic version pointer key: `kb:{tenant_id}:{kb_id}:latest_version`
  - Because Redis 7.2 Cluster hashes the first `{...}` substring (`{tenant_id}`), both keys for a given tenant automatically map to the **exact same Redis Cluster CRC16 hash slot**, enabling atomic single-RTT Lua (`EVALSHA`) or pipelined operations without `CROSSSLOT` errors.
- **TTL**: Bounded safety TTL (default `3600s` / 1 hour).
- **Coherence, Eventual Consistency & Causal Freshness (`min_version`)**:
  - Zero in-process L1 caching and zero Pub/Sub invalidation buses.
  - Immutable versioned snapshots (`kb:{tenant_id}:{kb_id}:v{version}`) never mutate once written. Upon committing version `v+1` in PostgreSQL, the service performs a post-commit write of `kb:{tenant_id}:{kb_id}:v{v+1}` to Redis Cluster and advances `kb:{tenant_id}:{kb_id}:latest_version` via a Lua monotonic guard (`if new_ver > cur_ver`).
  - **Bounded Eventual Consistency on Unconstrained Reads**: Because the PostgreSQL commit precedes the asynchronous/post-commit Redis update, a network fault or process crash between PostgreSQL commit and Redis write can leave `kb:{tenant_id}:{kb_id}:latest_version` (and the cached `active_index_pointer`) pointing to version `v` until a subsequent read/write repairs the cache or the 1-hour safety TTL expires.
  - **Read-Your-Writes Freshness via `min_version`**: Callers requiring strict causal freshness (such as post-mutation verifiers or promotion orchestrators knowing committed version `v+1`) pass `min_version = v+1` on `GetKnowledgeBase`. When `cached_version < min_version` (or on a cache miss), `GetKnowledgeBase` rejects the stale Redis entry, queries PostgreSQL directly, and backfills `kb:{tenant_id}:{kb_id}:v{v+1}` and `latest_version` in Redis Cluster.

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
