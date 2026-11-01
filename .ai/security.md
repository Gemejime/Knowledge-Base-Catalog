# Security Context - RAG Knowledge Base Catalog Service

## 1. Threat Model & Trust Boundaries

1. **Multi-Tenant Control-Plane Isolation**:
   - `rag-kb-catalog-service` governs metadata and index pointers across multiple enterprise tenants. A cross-tenant leakage or unauthorized pointer mutation could expose or corrupt proprietary vector corpora.
   - **Mitigation**: All PostgreSQL tables (`knowledge_bases`, `kb_migrations`, `outbox_events`) enforce compound primary keys (`(tenant_id, kb_id)`) or explicit `tenant_id` indices, and all Redis Cluster keys are prefixed with `kb:{tenant_id}:{id}:v{version}`. Repositories reject any operation missing an explicit, validated tenant context.
2. **Authentication vs. Authorization (AuthN vs. AuthZ)**:
   - **AuthN**: Edge API Gateway validates caller JWT signatures and propagates verified claims (`tenant_id`, `user_id`, `roles`) via gRPC metadata and HTTP headers.
   - **AuthZ (`internal/domain/policy/access_evaluator.go`)**: Coarse entity-level RBAC/ABAC (`Owner`, `Editor`, `Viewer`, `Auditor`) is evaluated before executing any administrative command (`CreateKnowledgeBase`, `UpdateKnowledgeBase`, `DeleteKnowledgeBase`, `TriggerReindexing`, `PromoteIndexPointer`, `AbortReindexing`). Fine-grained document ACL bitmaps are delegated strictly to `rag-search-orchestrator`.
3. **Destructive In-Place Vector Schema Mutations**:
   - Mutating vector dimensions (e.g., `768` to `1536`) or distance metrics (`COSINE` to `L2`) in-place would corrupt active HNSW indices and break live search queries.
   - **Mitigation (ADR-001)**: `immutability_guard.go` strictly rejects in-place structural edits with `ErrStructuralMutationDisallowed`, forcing all schema upgrades through the tracked Blue-Green migration state machine (`ACTIVE` -> `REINDEXING` -> `PROMOTED` / `ABORTED`).
4. **Distroless Non-Root Container Hardening**:
   - Production containers run on `gcr.io/distroless/static:nonroot` (non-root user, read-only root filesystem, zero shell or package manager binaries) with strict Kubernetes resource bounds (CPU Limit `500m`, Memory Limit `128MiB`).

---

## 2. Secured Logging Invariants

1. **Structured JSON Output (`log/slog`)**:
   - Every log record must be emitted as machine-parseable JSON via Go's `log/slog` package. Raw `fmt.Print*` or unstructured `log.Print*` statements are forbidden in production code and blocked by CI (`security-and-logging-audit`).
2. **Strict Credential & Token Redaction**:
   - **NEVER** log raw JWT bearer tokens, `Authorization` headers, PostgreSQL connection DSNs/passwords, Redis Cluster passwords, or sensitive customer PII/PHI metadata.
   - All custom configuration structs and transport middlewares must redact sensitive attributes (`"[REDACTED]"`) via `slog.LogValuer` or `ReplaceAttr` hooks prior to serialization.
3. **Sanitized Error Responses**:
   - Internal database errors, PgBouncer pool diagnostics, and stack traces must never be exposed in raw HTTP JSON bodies or gRPC status details returned to external callers.

---

## 3. Secrets & Configuration Management (12-Factor App)

- All environment-specific configurations and credentials (`DATABASE_URL`, `REDIS_CLUSTER_ADDRS`, `REDIS_PASSWORD`, `TEMPORAL_HOST`, `JWT_PUBLIC_KEY`) are injected via environment variables or secret store mounts.
- `.env` files, private keys (`*.pem`, `*.key`), and the `tasks/` architectural reference folder are strictly excluded from version control via `.gitignore`.

---

## 4. Git Commit Standards

- **Git Commit Frequency**: Commit code as you build. Every working session or phase must contain **at least two git commits**.
- **Conventional Commit Patterns**: All git commit messages must strictly adhere to the following prefixes:
  - `feat:` for new features 
  - `fix:` for bug fixes
  - `refactor:` for code refactoring
  - `docs:` for documentation updates
  - `tests:` for tests addition and modification
  - `chore:` for maintenance tasks and environment setup
