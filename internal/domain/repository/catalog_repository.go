package repository

import (
	"context"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
)

// IdempotencyRecord represents a tenant-scoped idempotency record stored in PostgreSQL.
type IdempotencyRecord struct {
	TenantID        string
	IdempotencyKey  string
	Operation       string
	RequestHash     string
	ResponsePayload []byte
	StatusCode      int
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

// ListFilter defines pagination and taxonomy filtering parameters for ListKnowledgeBases.
type ListFilter struct {
	TenantID        string
	TaxonomyFilters []string
	PageSize        int
	PageToken       string
}

// CatalogRepository defines the hexagonal persistence port for Knowledge Base lifecycles,
// Optimistic Concurrency Control (OCC), Blue-Green migrations, and atomic outbox commits.
type CatalogRepository interface {
	// GetIdempotencyRecord retrieves an unexpired idempotency record for (tenantID, key).
	GetIdempotencyRecord(ctx context.Context, tenantID, key string, now time.Time) (*IdempotencyRecord, error)

	// Create atomically persists a new KnowledgeBase, its KBCreated OutboxEvent, and optional IdempotencyRecord.
	Create(ctx context.Context, kb *entity.KnowledgeBase, outbox *event.OutboxEvent, idempotency *IdempotencyRecord) error

	// GetByID retrieves a KnowledgeBase by (tenantID, kbID), returning ErrKnowledgeBaseNotFound if missing or TOMBSTONED.
	GetByID(ctx context.Context, tenantID, kbID string) (*entity.KnowledgeBase, error)

	// List retrieves a paginated list of non-TOMBSTONED KnowledgeBases for a tenant filtered by taxonomy tags.
	List(ctx context.Context, filter ListFilter) ([]*entity.KnowledgeBase, string, error)

	// UpdateMetadata atomically updates mutable metadata fields under OCC (version = version + 1) and records KBUpdated in outbox_events.
	UpdateMetadata(ctx context.Context, kb *entity.KnowledgeBase, expectedVersion uint64, outbox *event.OutboxEvent) error

	// TriggerReindexing atomically transitions ACTIVE -> REINDEXING under OCC, inserts the kb_migrations row, and records ReindexInitiated in outbox_events.
	TriggerReindexing(ctx context.Context, kb *entity.KnowledgeBase, migration *entity.KBMigration, expectedVersion uint64, outbox *event.OutboxEvent) error

	// GetActiveMigration retrieves the active (REINDEXING) migration record for (tenantID, kbID), including authoritative delta_lag_records.
	GetActiveMigration(ctx context.Context, tenantID, kbID string) (*entity.KBMigration, error)

	// PromotePointer atomically verifies authoritative delta_lag_records == 0 in kb_migrations, transitions REINDEXING -> PROMOTED -> ACTIVE under OCC,
	// updates kb_migrations.state = 'PROMOTED', and records PointerPromoted in outbox_events.
	PromotePointer(ctx context.Context, tenantID, kbID string, expectedVersion uint64, clientVerifiedLag *uint64, now time.Time) (*entity.KnowledgeBase, error)

	// AbortReindexing atomically transitions REINDEXING -> ABORTED -> ACTIVE under OCC, updates kb_migrations.state = 'ABORTED',
	// and records ReindexAborted in outbox_events.
	AbortReindexing(ctx context.Context, tenantID, kbID string, expectedVersion uint64, reason string, now time.Time) (*entity.KnowledgeBase, error)

	// SoftDelete atomically transitions the KnowledgeBase to TOMBSTONED under OCC and records KBDeleted in outbox_events.
	SoftDelete(ctx context.Context, tenantID, kbID string, expectedVersion uint64, now time.Time) error
}

// VersionedCache defines the optional direct Redis 7.2 Cluster read/write cache port (ADR-003).
type VersionedCache interface {
	GetLatest(ctx context.Context, tenantID, kbID string, minVersion uint64) (*entity.KnowledgeBase, error)
	PutVersion(ctx context.Context, kb *entity.KnowledgeBase) error
}
