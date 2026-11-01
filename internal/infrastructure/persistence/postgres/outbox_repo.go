package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
)

// OutboxRepo inserts transactional CDC domain events into the outbox_events table (ADR-005).
type OutboxRepo struct{}

// NewOutboxRepo constructs an OutboxRepo.
func NewOutboxRepo() *OutboxRepo {
	return &OutboxRepo{}
}

// InsertTx writes an OutboxEvent inside the active PostgreSQL transaction (pgx.Tx).
func (r *OutboxRepo) InsertTx(ctx context.Context, tx pgx.Tx, evt *event.OutboxEvent) error {
	if evt == nil {
		return fmt.Errorf("%w: outbox event is required", entity.ErrInvalidInput)
	}
	if strings.TrimSpace(evt.TenantID) == "" {
		return fmt.Errorf("%w: outbox event tenant_id is required", entity.ErrInvalidInput)
	}

	const query = `
		INSERT INTO outbox_events (
			event_id,
			tenant_id,
			aggregate_type,
			aggregate_id,
			event_type,
			aggregate_version,
			payload,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	_, err := tx.Exec(
		ctx,
		query,
		evt.EventID,
		evt.TenantID,
		evt.AggregateType,
		evt.AggregateID,
		string(evt.EventType),
		evt.AggregateVersion,
		evt.Payload,
		evt.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event %s: %w", evt.EventType, err)
	}
	return nil
}
