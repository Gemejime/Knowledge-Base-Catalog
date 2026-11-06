package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
)

// CatalogRepo implements repository.CatalogRepository backed by PostgreSQL 16 + PgBouncer (exec mode).
type CatalogRepo struct {
	pool    *pgxpool.Pool
	outbox  *OutboxRepo
	metrics *observability.Metrics
}

// NewCatalogRepo constructs a PostgreSQL-backed CatalogRepo.
func NewCatalogRepo(pool *pgxpool.Pool, outbox *OutboxRepo, metrics *observability.Metrics) *CatalogRepo {
	if outbox == nil {
		outbox = NewOutboxRepo()
	}
	return &CatalogRepo{
		pool:    pool,
		outbox:  outbox,
		metrics: metrics,
	}
}

func requireTenant(tenantID string) (string, error) {
	t := strings.TrimSpace(tenantID)
	if t == "" {
		return "", fmt.Errorf("%w: tenant_id is required for repository operations", entity.ErrInsufficientPermissions)
	}
	return t, nil
}

// GetIdempotencyRecord retrieves an unexpired idempotency record for (tenantID, key).
func (r *CatalogRepo) GetIdempotencyRecord(ctx context.Context, tenantID, key string, now time.Time) (*repository.IdempotencyRecord, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT tenant_id, idempotency_key, operation, request_hash, response_payload, status_code, created_at, expires_at
		FROM idempotency_keys
		WHERE tenant_id = $1 AND idempotency_key = $2 AND expires_at > $3
	`
	var rec repository.IdempotencyRecord
	err = r.pool.QueryRow(ctx, q, t, key, now).Scan(
		&rec.TenantID,
		&rec.IdempotencyKey,
		&rec.Operation,
		&rec.RequestHash,
		&rec.ResponsePayload,
		&rec.StatusCode,
		&rec.CreatedAt,
		&rec.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query idempotency record: %w", err)
	}
	return &rec, nil
}

// Create atomically persists a new KnowledgeBase, its KBCreated OutboxEvent, and optional IdempotencyRecord.
func (r *CatalogRepo) Create(ctx context.Context, kb *entity.KnowledgeBase, outbox *event.OutboxEvent, idempotency *repository.IdempotencyRecord) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if idempotency != nil {
		const insertIdemp = `
			INSERT INTO idempotency_keys (
				tenant_id, idempotency_key, operation, request_hash, response_payload, status_code, created_at, expires_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, idempotency_key) DO UPDATE
			SET request_hash = EXCLUDED.request_hash,
			    response_payload = EXCLUDED.response_payload,
			    status_code = EXCLUDED.status_code,
			    created_at = EXCLUDED.created_at,
			    expires_at = EXCLUDED.expires_at
			WHERE idempotency_keys.expires_at <= $7
		`
		tag, err := tx.Exec(
			ctx,
			insertIdemp,
			t,
			idempotency.IdempotencyKey,
			idempotency.Operation,
			idempotency.RequestHash,
			idempotency.ResponsePayload,
			idempotency.StatusCode,
			idempotency.CreatedAt,
			idempotency.ExpiresAt,
		)
		if err != nil {
			return fmt.Errorf("failed to insert idempotency record: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return entity.ErrIdempotencyKeyConflict
		}
	}

	activeSchemaJSON, err := json.Marshal(kb.ActiveSchema)
	if err != nil {
		return err
	}
	profileJSON, err := json.Marshal(kb.RetrievalProfile)
	if err != nil {
		return err
	}
	tagsJSON, err := json.Marshal(kb.TaxonomyTags)
	if err != nil {
		return err
	}

	const insertKB = `
		INSERT INTO knowledge_bases (
			tenant_id, kb_id, name, description, version, state, routing_policy,
			active_index_pointer, candidate_index_pointer, active_schema, candidate_schema,
			retrieval_profile, taxonomy_tags, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, $11, $12, $13, $14)
	`
	_, err = tx.Exec(
		ctx,
		insertKB,
		t,
		kb.KBID,
		kb.Name,
		kb.Description,
		kb.Version,
		string(kb.State),
		string(kb.RoutingPolicy),
		kb.ActiveIndexPointer,
		kb.CandidateIndexPointer,
		activeSchemaJSON,
		profileJSON,
		tagsJSON,
		kb.CreatedAt,
		kb.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %s", entity.ErrDuplicateKBName, kb.Name)
		}
		return fmt.Errorf("failed to insert knowledge base: %w", err)
	}

	if err := r.outbox.InsertTx(ctx, tx, outbox); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// GetByID retrieves a KnowledgeBase by (tenantID, kbID), returning ErrKnowledgeBaseNotFound if missing or TOMBSTONED.
func (r *CatalogRepo) GetByID(ctx context.Context, tenantID, kbID string) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	const q = `
		SELECT tenant_id, kb_id, name, description, version, state, routing_policy,
		       active_index_pointer, candidate_index_pointer, active_schema, candidate_schema,
		       retrieval_profile, taxonomy_tags, created_at, updated_at
		FROM knowledge_bases
		WHERE tenant_id = $1 AND kb_id = $2 AND state != 'TOMBSTONED'
	`
	kb, err := scanKB(r.pool.QueryRow(ctx, q, t, kbID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	return kb, err
}

// List retrieves a paginated list of non-TOMBSTONED KnowledgeBases for a tenant filtered by taxonomy tags.
func (r *CatalogRepo) List(ctx context.Context, filter repository.ListFilter) ([]*entity.KnowledgeBase, string, error) {
	t, err := requireTenant(filter.TenantID)
	if err != nil {
		return nil, "", err
	}
	limit := filter.PageSize
	if limit <= 0 {
		limit = 20
	}

	tagsJSON, err := json.Marshal(filter.TaxonomyFilters)
	if err != nil {
		return nil, "", err
	}

	const q = `
		SELECT tenant_id, kb_id, name, description, version, state, routing_policy,
		       active_index_pointer, candidate_index_pointer, active_schema, candidate_schema,
		       retrieval_profile, taxonomy_tags, created_at, updated_at
		FROM knowledge_bases
		WHERE tenant_id = $1
		  AND state != 'TOMBSTONED'
		  AND ($2::text = '' OR kb_id::text > $2)
		  AND ($3::jsonb = '[]'::jsonb OR taxonomy_tags @> $3::jsonb)
		ORDER BY kb_id ASC
		LIMIT $4
	`
	rows, err := r.pool.Query(ctx, q, t, filter.PageToken, string(tagsJSON), limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list knowledge bases: %w", err)
	}
	defer rows.Close()

	var result []*entity.KnowledgeBase
	for rows.Next() {
		kb, err := scanKB(rows)
		if err != nil {
			return nil, "", err
		}
		result = append(result, kb)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextToken string
	if len(result) > limit {
		nextToken = result[limit-1].KBID
		result = result[:limit]
	}
	return result, nextToken, nil
}

// UpdateMetadata atomically updates mutable metadata fields under OCC (version = version + 1) and records KBUpdated in outbox_events.
func (r *CatalogRepo) UpdateMetadata(ctx context.Context, kb *entity.KnowledgeBase, expectedVersion uint64, outbox *event.OutboxEvent) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	profileJSON, err := json.Marshal(kb.RetrievalProfile)
	if err != nil {
		return err
	}
	tagsJSON, err := json.Marshal(kb.TaxonomyTags)
	if err != nil {
		return err
	}

	const updateSQL = `
		UPDATE knowledge_bases
		SET name = $4,
		    description = $5,
		    retrieval_profile = $6,
		    taxonomy_tags = $7,
		    version = version + 1,
		    updated_at = $8
		WHERE tenant_id = $1
		  AND kb_id = $2
		  AND version = $3
		  AND state != 'TOMBSTONED'
		RETURNING version, updated_at
	`
	err = tx.QueryRow(ctx, updateSQL, t, kb.KBID, expectedVersion, kb.Name, kb.Description, profileJSON, tagsJSON, kb.UpdatedAt).
		Scan(&kb.Version, &kb.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.disambiguateMissingOrConflictTx(ctx, tx, t, kb.KBID, expectedVersion, "UpdateKnowledgeBase")
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: %s", entity.ErrDuplicateKBName, kb.Name)
		}
		return fmt.Errorf("failed to update knowledge base metadata: %w", err)
	}

	if err := r.outbox.InsertTx(ctx, tx, outbox); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// TriggerReindexing atomically transitions ACTIVE -> REINDEXING under OCC, inserts the kb_migrations row, and records ReindexInitiated in outbox_events.
func (r *CatalogRepo) TriggerReindexing(ctx context.Context, kb *entity.KnowledgeBase, migration *entity.KBMigration, expectedVersion uint64, outbox *event.OutboxEvent) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	candidateSchemaJSON, err := json.Marshal(kb.CandidateSchema)
	if err != nil {
		return err
	}

	const updateKB = `
		UPDATE knowledge_bases
		SET state = 'REINDEXING',
		    routing_policy = 'DUAL_WRITE',
		    candidate_index_pointer = $4,
		    candidate_schema = $5,
		    version = version + 1,
		    updated_at = $6
		WHERE tenant_id = $1
		  AND kb_id = $2
		  AND version = $3
		  AND state = 'ACTIVE'
		RETURNING version, updated_at
	`
	err = tx.QueryRow(ctx, updateKB, t, kb.KBID, expectedVersion, kb.CandidateIndexPointer, candidateSchemaJSON, kb.UpdatedAt).
		Scan(&kb.Version, &kb.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.disambiguateStateOrConflictTx(ctx, tx, t, kb.KBID, expectedVersion, entity.StateActive, "TriggerReindexing")
	}
	if err != nil {
		return fmt.Errorf("failed to update knowledge base for reindexing: %w", err)
	}

	srcSchemaJSON, err := json.Marshal(migration.SourceSchema)
	if err != nil {
		return err
	}
	tgtSchemaJSON, err := json.Marshal(migration.TargetSchema)
	if err != nil {
		return err
	}

	const insertMig = `
		INSERT INTO kb_migrations (
			tenant_id, migration_id, kb_id, source_green_pointer, target_blue_pointer,
			source_schema, target_schema, state, delta_lag_records, temporal_workflow_id,
			abort_reason, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'REINDEXING', 0, $8, '', $9, $10)
	`
	_, err = tx.Exec(
		ctx,
		insertMig,
		t,
		migration.MigrationID,
		migration.KBID,
		migration.SourceGreenPointer,
		migration.TargetBluePointer,
		srcSchemaJSON,
		tgtSchemaJSON,
		migration.TemporalWorkflowID,
		migration.CreatedAt,
		migration.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w: an active REINDEXING migration already exists for knowledge base %s", entity.ErrInvalidStateTransition, kb.KBID)
		}
		return fmt.Errorf("failed to insert kb_migrations record: %w", err)
	}

	if err := r.outbox.InsertTx(ctx, tx, outbox); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if r.metrics != nil {
		r.metrics.ActiveMigrations.WithLabelValues(t).Inc()
		r.metrics.MigrationDeltaLag.WithLabelValues(t, kb.KBID).Set(0)
	}
	return nil
}

// GetActiveMigration retrieves the active (REINDEXING) migration record for (tenantID, kbID), including authoritative delta_lag_records.
func (r *CatalogRepo) GetActiveMigration(ctx context.Context, tenantID, kbID string) (*entity.KBMigration, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	return r.getActiveMigrationQuery(ctx, r.pool, t, kbID)
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *CatalogRepo) getActiveMigrationQuery(ctx context.Context, q rowQuerier, tenantID, kbID string) (*entity.KBMigration, error) {
	const sqlStr = `
		SELECT tenant_id, migration_id, kb_id, source_green_pointer, target_blue_pointer,
		       source_schema, target_schema, state, delta_lag_records, backfill_completed, write_fence_drained,
		       temporal_workflow_id, abort_reason, created_at, updated_at
		FROM kb_migrations
		WHERE tenant_id = $1 AND kb_id = $2 AND state = 'REINDEXING'
	`
	var m entity.KBMigration
	var srcBytes, tgtBytes []byte
	var stateStr string
	var lag int64
	err := q.QueryRow(ctx, sqlStr, tenantID, kbID).Scan(
		&m.TenantID,
		&m.MigrationID,
		&m.KBID,
		&m.SourceGreenPointer,
		&m.TargetBluePointer,
		&srcBytes,
		&tgtBytes,
		&stateStr,
		&lag,
		&m.BackfillCompleted,
		&m.WriteFenceDrained,
		&m.TemporalWorkflowID,
		&m.AbortReason,
		&m.CreatedAt,
		&m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no active REINDEXING migration found for kb %s", entity.ErrInvalidStateTransition, kbID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active migration: %w", err)
	}
	if err := json.Unmarshal(srcBytes, &m.SourceSchema); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tgtBytes, &m.TargetSchema); err != nil {
		return nil, err
	}
	m.State = entity.LifecycleState(stateStr)
	if lag > 0 {
		m.DeltaLagRecords = uint64(lag)
	}
	return &m, nil
}

// UpdateMigrationProgress updates authoritative delta_lag_records, backfill_completed, and write_fence_drained for an active migration.
func (r *CatalogRepo) UpdateMigrationProgress(ctx context.Context, tenantID, kbID string, deltaLag uint64, backfillCompleted, writeFenceDrained bool) error {
	t, err := requireTenant(tenantID)
	if err != nil {
		return err
	}

	const q = `
		UPDATE kb_migrations
		SET delta_lag_records = $3,
		    backfill_completed = $4,
		    write_fence_drained = $5,
		    updated_at = $6
		WHERE tenant_id = $1 AND kb_id = $2 AND state = 'REINDEXING'
	`
	tag, err := r.pool.Exec(ctx, q, t, kbID, deltaLag, backfillCompleted, writeFenceDrained, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to update migration progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no active REINDEXING migration found for kb %s", entity.ErrInvalidStateTransition, kbID)
	}

	if r.metrics != nil {
		r.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(float64(deltaLag))
	}
	return nil
}

// PromotePointer atomically verifies authoritative delta_lag_records == 0 in kb_migrations,
// transitions REINDEXING -> PROMOTED -> ACTIVE under OCC, updates kb_migrations.state = 'PROMOTED',
// and records PointerPromoted in outbox_events.
func (r *CatalogRepo) PromotePointer(ctx context.Context, tenantID, kbID string, expectedVersion uint64, clientVerifiedLag *uint64, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	kb, err := r.getByIDTx(ctx, tx, t, kbID)
	if err != nil {
		return nil, err
	}
	if kb.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: knowledge base must be in REINDEXING state to promote pointer (current: %s)", entity.ErrInvalidStateTransition, kb.State)
	}
	if kb.Version != expectedVersion {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "PromoteIndexPointer")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, kb.Version)
	}

	migration, err := r.getActiveMigrationQuery(ctx, tx, t, kbID)
	if err != nil {
		return nil, err
	}

	oldGreenPointer := kb.ActiveIndexPointer
	if err := kb.PromotePointer(migration, expectedVersion, migration.DeltaLagRecords, clientVerifiedLag, now); err != nil {
		return nil, err
	}

	activeSchemaJSON, err := json.Marshal(kb.ActiveSchema)
	if err != nil {
		return nil, err
	}

	const updateKB = `
		UPDATE knowledge_bases
		SET state = 'ACTIVE',
		    routing_policy = 'GREEN_ONLY',
		    active_index_pointer = $4,
		    candidate_index_pointer = '',
		    active_schema = $5,
		    candidate_schema = NULL,
		    version = version + 1,
		    updated_at = $6
		WHERE tenant_id = $1 AND kb_id = $2 AND version = $3 AND state = 'REINDEXING'
		RETURNING version, updated_at
	`
	err = tx.QueryRow(ctx, updateKB, t, kbID, expectedVersion, kb.ActiveIndexPointer, activeSchemaJSON, kb.UpdatedAt).
		Scan(&kb.Version, &kb.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "PromoteIndexPointer")
		}
		return nil, entity.ErrVersionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update knowledge base on pointer promotion: %w", err)
	}

	const updateMig = `
		UPDATE kb_migrations
		SET state = 'PROMOTED',
		    updated_at = $3
		WHERE tenant_id = $1 AND migration_id = $2 AND state = 'REINDEXING' AND delta_lag_records = 0 AND backfill_completed = TRUE AND write_fence_drained = TRUE
	`
	tag, err := tx.Exec(ctx, updateMig, t, migration.MigrationID, now)
	if err != nil {
		return nil, fmt.Errorf("failed to mark migration PROMOTED: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, entity.ErrDeltaLagNotZero
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventPointerPromoted, map[string]any{
		"migration_id":          migration.MigrationID,
		"retired_green_pointer": oldGreenPointer,
		"promoted_blue_pointer": kb.ActiveIndexPointer,
		"verified_delta_lag":    uint64(0),
	}, now)
	if err != nil {
		return nil, err
	}
	if err := r.outbox.InsertTx(ctx, tx, outboxEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if r.metrics != nil {
		r.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		r.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}
	return kb, nil
}

// AbortReindexing atomically transitions REINDEXING -> ABORTED -> ACTIVE under OCC,
// updates kb_migrations.state = 'ABORTED', and records ReindexAborted in outbox_events.
func (r *CatalogRepo) AbortReindexing(ctx context.Context, tenantID, kbID string, expectedVersion uint64, reason string, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	kb, err := r.getByIDTx(ctx, tx, t, kbID)
	if err != nil {
		return nil, err
	}
	if kb.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: knowledge base must be in REINDEXING state to abort (current: %s)", entity.ErrInvalidStateTransition, kb.State)
	}
	if kb.Version != expectedVersion {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "AbortReindexing")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, kb.Version)
	}

	migration, err := r.getActiveMigrationQuery(ctx, tx, t, kbID)
	if err != nil {
		return nil, err
	}

	orphanedBlue, err := kb.AbortReindexing(migration, expectedVersion, reason, now)
	if err != nil {
		return nil, err
	}

	const updateKB = `
		UPDATE knowledge_bases
		SET state = 'ACTIVE',
		    routing_policy = 'GREEN_ONLY',
		    candidate_index_pointer = '',
		    candidate_schema = NULL,
		    version = version + 1,
		    updated_at = $4
		WHERE tenant_id = $1 AND kb_id = $2 AND version = $3 AND state = 'REINDEXING'
		RETURNING version, updated_at
	`
	err = tx.QueryRow(ctx, updateKB, t, kbID, expectedVersion, kb.UpdatedAt).
		Scan(&kb.Version, &kb.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "AbortReindexing")
		}
		return nil, entity.ErrVersionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update knowledge base on abort reindexing: %w", err)
	}

	const updateMig = `
		UPDATE kb_migrations
		SET state = 'ABORTED',
		    abort_reason = $3,
		    updated_at = $4
		WHERE tenant_id = $1 AND migration_id = $2 AND state = 'REINDEXING'
	`
	if _, err := tx.Exec(ctx, updateMig, t, migration.MigrationID, reason, now); err != nil {
		return nil, fmt.Errorf("failed to mark migration ABORTED: %w", err)
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventReindexAborted, map[string]any{
		"migration_id":          migration.MigrationID,
		"orphaned_blue_pointer": orphanedBlue,
		"abort_reason":          reason,
	}, now)
	if err != nil {
		return nil, err
	}
	if err := r.outbox.InsertTx(ctx, tx, outboxEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if r.metrics != nil {
		r.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		r.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}
	return kb, nil
}

// SoftDelete atomically transitions the KnowledgeBase to TOMBSTONED under OCC and records KBDeleted in outbox_events.
func (r *CatalogRepo) SoftDelete(ctx context.Context, tenantID, kbID string, expectedVersion uint64, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	kb, err := r.getByIDTx(ctx, tx, t, kbID)
	if err != nil {
		return nil, err
	}
	if kb.Version != expectedVersion {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "DeleteKnowledgeBase")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, kb.Version)
	}

	wasReindexing := kb.State == entity.StateReindexing
	capturedCandidate := kb.CandidateIndexPointer

	if err := kb.MarkTombstoned(expectedVersion, now); err != nil {
		return nil, err
	}

	const deleteSQL = `
		UPDATE knowledge_bases
		SET state = 'TOMBSTONED',
		    routing_policy = 'GREEN_ONLY',
		    candidate_index_pointer = '',
		    candidate_schema = NULL,
		    version = version + 1,
		    updated_at = $4
		WHERE tenant_id = $1 AND kb_id = $2 AND version = $3 AND state != 'TOMBSTONED'
		RETURNING version, updated_at
	`
	err = tx.QueryRow(ctx, deleteSQL, t, kbID, expectedVersion, kb.UpdatedAt).
		Scan(&kb.Version, &kb.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if r.metrics != nil {
			r.metrics.RecordOptimisticConflict(t, "DeleteKnowledgeBase")
		}
		return nil, entity.ErrVersionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("failed to tombstone knowledge base: %w", err)
	}

	if wasReindexing {
		const abortMigSQL = `
			UPDATE kb_migrations
			SET state = 'ABORTED',
			    abort_reason = 'knowledge base deleted',
			    updated_at = $3
			WHERE tenant_id = $1 AND kb_id = $2 AND state = 'REINDEXING'
		`
		if _, err := tx.Exec(ctx, abortMigSQL, t, kbID, now); err != nil {
			return nil, fmt.Errorf("failed to abort active migration on delete: %w", err)
		}
	}

	cancelledCount := 0
	if wasReindexing {
		cancelledCount = 1
	}
	extraPayload := map[string]any{
		"candidate_index_pointer":     capturedCandidate,
		"active_migrations_cancelled": cancelledCount,
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventKBDeleted, extraPayload, now)
	if err != nil {
		return nil, err
	}
	if err := r.outbox.InsertTx(ctx, tx, outboxEvt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if wasReindexing && r.metrics != nil {
		r.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		r.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}

	return kb, nil
}

func (r *CatalogRepo) getByIDTx(ctx context.Context, tx pgx.Tx, tenantID, kbID string) (*entity.KnowledgeBase, error) {
	const q = `
		SELECT tenant_id, kb_id, name, description, version, state, routing_policy,
		       active_index_pointer, candidate_index_pointer, active_schema, candidate_schema,
		       retrieval_profile, taxonomy_tags, created_at, updated_at
		FROM knowledge_bases
		WHERE tenant_id = $1 AND kb_id = $2 AND state != 'TOMBSTONED'
		FOR NO KEY UPDATE
	`
	kb, err := scanKB(tx.QueryRow(ctx, q, tenantID, kbID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	return kb, err
}

func (r *CatalogRepo) disambiguateMissingOrConflictTx(ctx context.Context, tx pgx.Tx, tenantID, kbID string, expectedVersion uint64, operation string) error {
	const checkSQL = `SELECT version, state FROM knowledge_bases WHERE tenant_id = $1 AND kb_id = $2 FOR NO KEY UPDATE`
	var actualVersion uint64
	var stateStr string
	err := tx.QueryRow(ctx, checkSQL, tenantID, kbID).Scan(&actualVersion, &stateStr)
	if errors.Is(err, pgx.ErrNoRows) || entity.LifecycleState(stateStr) == entity.StateTombstoned {
		return entity.ErrKnowledgeBaseNotFound
	}
	if err != nil {
		return err
	}
	if r.metrics != nil {
		r.metrics.RecordOptimisticConflict(tenantID, operation)
	}
	return fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, actualVersion)
}

func (r *CatalogRepo) disambiguateStateOrConflictTx(ctx context.Context, tx pgx.Tx, tenantID, kbID string, expectedVersion uint64, requiredState entity.LifecycleState, operation string) error {
	const checkSQL = `SELECT version, state FROM knowledge_bases WHERE tenant_id = $1 AND kb_id = $2 FOR NO KEY UPDATE`
	var actualVersion uint64
	var stateStr string
	err := tx.QueryRow(ctx, checkSQL, tenantID, kbID).Scan(&actualVersion, &stateStr)
	if errors.Is(err, pgx.ErrNoRows) || entity.LifecycleState(stateStr) == entity.StateTombstoned {
		return entity.ErrKnowledgeBaseNotFound
	}
	if err != nil {
		return err
	}
	if entity.LifecycleState(stateStr) != requiredState {
		return fmt.Errorf("%w: knowledge base is in state %s (required %s)", entity.ErrInvalidStateTransition, stateStr, requiredState)
	}
	if r.metrics != nil {
		r.metrics.RecordOptimisticConflict(tenantID, operation)
	}
	return fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, actualVersion)
}

func scanKB(row pgx.Row) (*entity.KnowledgeBase, error) {
	var kb entity.KnowledgeBase
	var stateStr, routingStr string
	var activeSchemaBytes, candidateSchemaBytes, profileBytes, tagsBytes []byte

	err := row.Scan(
		&kb.TenantID,
		&kb.KBID,
		&kb.Name,
		&kb.Description,
		&kb.Version,
		&stateStr,
		&routingStr,
		&kb.ActiveIndexPointer,
		&kb.CandidateIndexPointer,
		&activeSchemaBytes,
		&candidateSchemaBytes,
		&profileBytes,
		&tagsBytes,
		&kb.CreatedAt,
		&kb.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	kb.State = entity.LifecycleState(stateStr)
	kb.RoutingPolicy = entity.RoutingPolicy(routingStr)

	if err := json.Unmarshal(activeSchemaBytes, &kb.ActiveSchema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal active_schema: %w", err)
	}
	if len(candidateSchemaBytes) > 0 && string(candidateSchemaBytes) != "null" {
		var cs entity.VectorSchema
		if err := json.Unmarshal(candidateSchemaBytes, &cs); err != nil {
			return nil, fmt.Errorf("failed to unmarshal candidate_schema: %w", err)
		}
		kb.CandidateSchema = &cs
	}
	if err := json.Unmarshal(profileBytes, &kb.RetrievalProfile); err != nil {
		return nil, fmt.Errorf("failed to unmarshal retrieval_profile: %w", err)
	}
	if len(tagsBytes) > 0 {
		if err := json.Unmarshal(tagsBytes, &kb.TaxonomyTags); err != nil {
			return nil, fmt.Errorf("failed to unmarshal taxonomy_tags: %w", err)
		}
	}
	if kb.TaxonomyTags == nil {
		kb.TaxonomyTags = []string{}
	}
	return &kb, nil
}

// ---------------------------------------------------------------------------
// Concurrency-Safe In-Memory Transactional Repository (Unit Tests & Standalone Mode)
// ---------------------------------------------------------------------------

// InMemoryCatalogRepo provides a race-free, ACID-equivalent in-memory implementation
// of repository.CatalogRepository for unit/transport test suites and standalone execution.
type InMemoryCatalogRepo struct {
	mu          sync.RWMutex
	kbs         map[string]*entity.KnowledgeBase         // key: tenantID + ":" + kbID
	migrations  map[string]*entity.KBMigration           // key: tenantID + ":" + kbID (active REINDEXING)
	migHistory  map[string]*entity.KBMigration           // key: tenantID + ":" + migrationID
	outbox      []*event.OutboxEvent                     // ordered append-only WAL outbox
	idempotency map[string]*repository.IdempotencyRecord // key: tenantID + ":" + idempotencyKey
	metrics     *observability.Metrics
}

// NewInMemoryCatalogRepo creates an initialized InMemoryCatalogRepo.
func NewInMemoryCatalogRepo(metrics *observability.Metrics) *InMemoryCatalogRepo {
	return &InMemoryCatalogRepo{
		kbs:         make(map[string]*entity.KnowledgeBase),
		migrations:  make(map[string]*entity.KBMigration),
		migHistory:  make(map[string]*entity.KBMigration),
		outbox:      make([]*event.OutboxEvent, 0),
		idempotency: make(map[string]*repository.IdempotencyRecord),
		metrics:     metrics,
	}
}

func compositeKey(tenantID, id string) string {
	return tenantID + ":" + id
}

// UpdateMigrationProgress updates authoritative delta_lag_records, backfill_completed, and write_fence_drained for an active migration.
func (m *InMemoryCatalogRepo) UpdateMigrationProgress(_ context.Context, tenantID, kbID string, deltaLag uint64, backfillCompleted, writeFenceDrained bool) error {
	t, err := requireTenant(tenantID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	mig, ok := m.migrations[compositeKey(t, kbID)]
	if !ok || mig.State != entity.StateReindexing {
		return fmt.Errorf("%w: no active REINDEXING migration found for kb %s", entity.ErrInvalidStateTransition, kbID)
	}

	mig.DeltaLagRecords = deltaLag
	mig.BackfillCompleted = backfillCompleted
	mig.WriteFenceDrained = writeFenceDrained
	mig.UpdatedAt = time.Now().UTC()

	if m.metrics != nil {
		m.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(float64(deltaLag))
	}
	return nil
}

// SetMigrationDeltaLag updates the authoritative delta_lag_records on the active migration for testing ADR-002.
func (m *InMemoryCatalogRepo) SetMigrationDeltaLag(tenantID, kbID string, lag uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mig, ok := m.migrations[compositeKey(tenantID, kbID)]
	if !ok || mig.State != entity.StateReindexing {
		return entity.ErrInvalidStateTransition
	}
	mig.DeltaLagRecords = lag
	if m.metrics != nil {
		m.metrics.MigrationDeltaLag.WithLabelValues(tenantID, kbID).Set(float64(lag))
	}
	return nil
}

// SetActiveMigrationBackfillAndFence updates backfill_completed and write_fence_drained on the active migration for testing.
func (m *InMemoryCatalogRepo) SetActiveMigrationBackfillAndFence(tenantID, kbID string, backfillDone, fenceDrained bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mig, ok := m.migrations[compositeKey(tenantID, kbID)]
	if !ok || mig.State != entity.StateReindexing {
		return entity.ErrInvalidStateTransition
	}
	mig.BackfillCompleted = backfillDone
	mig.WriteFenceDrained = fenceDrained
	return nil
}

// OutboxEvents returns a snapshot of all committed outbox events for verification in tests.
func (m *InMemoryCatalogRepo) OutboxEvents() []*event.OutboxEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*event.OutboxEvent, len(m.outbox))
	copy(out, m.outbox)
	return out
}

// GetIdempotencyRecord retrieves an unexpired idempotency record for (tenantID, key).
func (m *InMemoryCatalogRepo) GetIdempotencyRecord(_ context.Context, tenantID, key string, now time.Time) (*repository.IdempotencyRecord, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.idempotency[compositeKey(t, key)]
	if !ok || !rec.ExpiresAt.After(now) {
		return nil, nil
	}
	cp := *rec
	cp.ResponsePayload = append([]byte(nil), rec.ResponsePayload...)
	return &cp, nil
}

// Create atomically stores a new KnowledgeBase, its KBCreated outbox event, and optional IdempotencyRecord.
func (m *InMemoryCatalogRepo) Create(_ context.Context, kb *entity.KnowledgeBase, outbox *event.OutboxEvent, idempotency *repository.IdempotencyRecord) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, existing := range m.kbs {
		if existing.TenantID == t && existing.Name == kb.Name && existing.State != entity.StateTombstoned {
			return fmt.Errorf("%w: %s", entity.ErrDuplicateKBName, kb.Name)
		}
	}

	m.kbs[compositeKey(t, kb.KBID)] = dto.CloneKB(kb)
	if outbox != nil {
		evtCopy := *outbox
		evtCopy.Payload = append([]byte(nil), outbox.Payload...)
		m.outbox = append(m.outbox, &evtCopy)
	}
	if idempotency != nil {
		idCopy := *idempotency
		idCopy.ResponsePayload = append([]byte(nil), idempotency.ResponsePayload...)
		m.idempotency[compositeKey(t, idempotency.IdempotencyKey)] = &idCopy
	}
	return nil
}

// GetByID retrieves a KnowledgeBase by (tenantID, kbID).
func (m *InMemoryCatalogRepo) GetByID(_ context.Context, tenantID, kbID string) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	kb, ok := m.kbs[compositeKey(t, kbID)]
	if !ok || kb.State == entity.StateTombstoned {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	return dto.CloneKB(kb), nil
}

// List retrieves a paginated list of non-TOMBSTONED KnowledgeBases for a tenant filtered by taxonomy tags.
func (m *InMemoryCatalogRepo) List(_ context.Context, filter repository.ListFilter) ([]*entity.KnowledgeBase, string, error) {
	t, err := requireTenant(filter.TenantID)
	if err != nil {
		return nil, "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var candidates []*entity.KnowledgeBase
	for _, kb := range m.kbs {
		if kb.TenantID != t || kb.State == entity.StateTombstoned {
			continue
		}
		if filter.PageToken != "" && kb.KBID <= filter.PageToken {
			continue
		}
		if !containsAllTags(kb.TaxonomyTags, filter.TaxonomyFilters) {
			continue
		}
		candidates = append(candidates, dto.CloneKB(kb))
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].KBID < candidates[j].KBID
	})

	limit := filter.PageSize
	if limit <= 0 {
		limit = 20
	}
	var nextToken string
	if len(candidates) > limit {
		nextToken = candidates[limit-1].KBID
		candidates = candidates[:limit]
	}
	return candidates, nextToken, nil
}

func containsAllTags(haystack, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(haystack))
	for _, t := range haystack {
		set[t] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// UpdateMetadata atomically updates mutable metadata fields under OCC.
func (m *InMemoryCatalogRepo) UpdateMetadata(_ context.Context, kb *entity.KnowledgeBase, expectedVersion uint64, outbox *event.OutboxEvent) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := compositeKey(t, kb.KBID)
	existing, ok := m.kbs[key]
	if !ok || existing.State == entity.StateTombstoned {
		return entity.ErrKnowledgeBaseNotFound
	}
	if existing.Version != expectedVersion {
		if m.metrics != nil {
			m.metrics.RecordOptimisticConflict(t, "UpdateKnowledgeBase")
		}
		return fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, existing.Version)
	}
	if kb.Name != existing.Name {
		for _, other := range m.kbs {
			if other.TenantID == t && other.KBID != kb.KBID && other.Name == kb.Name && other.State != entity.StateTombstoned {
				return fmt.Errorf("%w: %s", entity.ErrDuplicateKBName, kb.Name)
			}
		}
	}

	m.kbs[key] = dto.CloneKB(kb)
	if outbox != nil {
		evtCopy := *outbox
		evtCopy.Payload = append([]byte(nil), outbox.Payload...)
		m.outbox = append(m.outbox, &evtCopy)
	}
	return nil
}

// TriggerReindexing atomically transitions ACTIVE -> REINDEXING under OCC.
func (m *InMemoryCatalogRepo) TriggerReindexing(_ context.Context, kb *entity.KnowledgeBase, migration *entity.KBMigration, expectedVersion uint64, outbox *event.OutboxEvent) error {
	t, err := requireTenant(kb.TenantID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := compositeKey(t, kb.KBID)
	existing, ok := m.kbs[key]
	if !ok || existing.State == entity.StateTombstoned {
		return entity.ErrKnowledgeBaseNotFound
	}
	if existing.State != entity.StateActive {
		return fmt.Errorf("%w: knowledge base is in state %s", entity.ErrInvalidStateTransition, existing.State)
	}
	if existing.Version != expectedVersion {
		if m.metrics != nil {
			m.metrics.RecordOptimisticConflict(t, "TriggerReindexing")
		}
		return fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, existing.Version)
	}
	if activeMig, hasActive := m.migrations[key]; hasActive && activeMig.State == entity.StateReindexing {
		return fmt.Errorf("%w: active migration already exists for kb %s", entity.ErrInvalidStateTransition, kb.KBID)
	}

	m.kbs[key] = dto.CloneKB(kb)
	migCopy := *migration
	m.migrations[key] = &migCopy
	m.migHistory[compositeKey(t, migration.MigrationID)] = &migCopy

	if outbox != nil {
		evtCopy := *outbox
		evtCopy.Payload = append([]byte(nil), outbox.Payload...)
		m.outbox = append(m.outbox, &evtCopy)
	}
	if m.metrics != nil {
		m.metrics.ActiveMigrations.WithLabelValues(t).Inc()
		m.metrics.MigrationDeltaLag.WithLabelValues(t, kb.KBID).Set(0)
	}
	return nil
}

// GetActiveMigration retrieves the active (REINDEXING) migration record for (tenantID, kbID).
func (m *InMemoryCatalogRepo) GetActiveMigration(_ context.Context, tenantID, kbID string) (*entity.KBMigration, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	mig, ok := m.migrations[compositeKey(t, kbID)]
	if !ok || mig.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: no active REINDEXING migration found for kb %s", entity.ErrInvalidStateTransition, kbID)
	}
	cp := *mig
	return &cp, nil
}

// PromotePointer atomically verifies authoritative delta_lag_records == 0 and switches pointer to Blue.
func (m *InMemoryCatalogRepo) PromotePointer(_ context.Context, tenantID, kbID string, expectedVersion uint64, clientVerifiedLag *uint64, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := compositeKey(t, kbID)
	existing, ok := m.kbs[key]
	if !ok || existing.State == entity.StateTombstoned {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	if existing.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: knowledge base must be in REINDEXING state to promote pointer (current: %s)", entity.ErrInvalidStateTransition, existing.State)
	}
	if existing.Version != expectedVersion {
		if m.metrics != nil {
			m.metrics.RecordOptimisticConflict(t, "PromoteIndexPointer")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, existing.Version)
	}

	mig, ok := m.migrations[key]
	if !ok || mig.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: no active REINDEXING migration found", entity.ErrInvalidStateTransition)
	}

	kbCopy := dto.CloneKB(existing)
	oldGreen := kbCopy.ActiveIndexPointer
	if err := kbCopy.PromotePointer(mig, expectedVersion, mig.DeltaLagRecords, clientVerifiedLag, now); err != nil {
		return nil, err
	}

	outboxEvt, err := event.NewOutboxEvent(kbCopy, event.EventPointerPromoted, map[string]any{
		"migration_id":          mig.MigrationID,
		"retired_green_pointer": oldGreen,
		"promoted_blue_pointer": kbCopy.ActiveIndexPointer,
		"verified_delta_lag":    uint64(0),
	}, now)
	if err != nil {
		return nil, err
	}

	m.kbs[key] = dto.CloneKB(kbCopy)
	delete(m.migrations, key)
	m.outbox = append(m.outbox, outboxEvt)

	if m.metrics != nil {
		m.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		m.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}
	return dto.CloneKB(kbCopy), nil
}

// AbortReindexing atomically transitions REINDEXING -> ABORTED -> ACTIVE under OCC.
func (m *InMemoryCatalogRepo) AbortReindexing(_ context.Context, tenantID, kbID string, expectedVersion uint64, reason string, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := compositeKey(t, kbID)
	existing, ok := m.kbs[key]
	if !ok || existing.State == entity.StateTombstoned {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	if existing.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: knowledge base must be in REINDEXING state to abort (current: %s)", entity.ErrInvalidStateTransition, existing.State)
	}
	if existing.Version != expectedVersion {
		if m.metrics != nil {
			m.metrics.RecordOptimisticConflict(t, "AbortReindexing")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, existing.Version)
	}

	mig, ok := m.migrations[key]
	if !ok || mig.State != entity.StateReindexing {
		return nil, fmt.Errorf("%w: no active REINDEXING migration found", entity.ErrInvalidStateTransition)
	}

	kbCopy := dto.CloneKB(existing)
	orphanedBlue, err := kbCopy.AbortReindexing(mig, expectedVersion, reason, now)
	if err != nil {
		return nil, err
	}

	outboxEvt, err := event.NewOutboxEvent(kbCopy, event.EventReindexAborted, map[string]any{
		"migration_id":          mig.MigrationID,
		"orphaned_blue_pointer": orphanedBlue,
		"abort_reason":          reason,
	}, now)
	if err != nil {
		return nil, err
	}

	m.kbs[key] = dto.CloneKB(kbCopy)
	delete(m.migrations, key)
	m.outbox = append(m.outbox, outboxEvt)

	if m.metrics != nil {
		m.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		m.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}
	return dto.CloneKB(kbCopy), nil
}

// SoftDelete atomically transitions the KnowledgeBase to TOMBSTONED under OCC.
func (m *InMemoryCatalogRepo) SoftDelete(_ context.Context, tenantID, kbID string, expectedVersion uint64, now time.Time) (*entity.KnowledgeBase, error) {
	t, err := requireTenant(tenantID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := compositeKey(t, kbID)
	existing, ok := m.kbs[key]
	if !ok || existing.State == entity.StateTombstoned {
		return nil, entity.ErrKnowledgeBaseNotFound
	}
	if existing.Version != expectedVersion {
		if m.metrics != nil {
			m.metrics.RecordOptimisticConflict(t, "DeleteKnowledgeBase")
		}
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, expectedVersion, existing.Version)
	}

	wasReindexing := existing.State == entity.StateReindexing
	capturedCandidate := existing.CandidateIndexPointer

	kbCopy := dto.CloneKB(existing)
	if err := kbCopy.MarkTombstoned(expectedVersion, now); err != nil {
		return nil, err
	}

	cancelledCount := 0
	if wasReindexing {
		cancelledCount = 1
	}
	extraPayload := map[string]any{
		"candidate_index_pointer":     capturedCandidate,
		"active_migrations_cancelled": cancelledCount,
	}

	outboxEvt, err := event.NewOutboxEvent(kbCopy, event.EventKBDeleted, extraPayload, now)
	if err != nil {
		return nil, err
	}

	m.kbs[key] = dto.CloneKB(kbCopy)
	delete(m.migrations, key)
	m.outbox = append(m.outbox, outboxEvt)

	if wasReindexing && m.metrics != nil {
		m.metrics.ActiveMigrations.WithLabelValues(t).Dec()
		m.metrics.MigrationDeltaLag.WithLabelValues(t, kbID).Set(0)
	}

	return dto.CloneKB(kbCopy), nil
}
