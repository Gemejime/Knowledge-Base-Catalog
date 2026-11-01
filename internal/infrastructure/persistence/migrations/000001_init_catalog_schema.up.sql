-- 000001_init_catalog_schema.up.sql
-- Authoritative PostgreSQL 16 Schema for rag-kb-catalog-service
-- Enforces strict tenant_id isolation, Optimistic Concurrency Control (version),
-- Blue-Green migration state tracking, transactional CDC outbox (Debezium WAL),
-- and tenant-scoped idempotency deduplication.

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

