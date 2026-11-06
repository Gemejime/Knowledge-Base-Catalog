-- 000001_init_catalog_schema.down.sql
-- Deterministic teardown of rag-kb-catalog-service relational schema

DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS outbox_events;
DROP INDEX IF EXISTS uq_kb_migrations_one_active;
DROP INDEX IF EXISTS idx_kb_migrations_active;
DROP TABLE IF EXISTS kb_migrations;
DROP INDEX IF EXISTS idx_kb_tenant_state;
DROP INDEX IF EXISTS idx_kb_tenant_taxonomy;
DROP INDEX IF EXISTS uq_kb_tenant_name;
DROP TABLE IF EXISTS knowledge_bases;

