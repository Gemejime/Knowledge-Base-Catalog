package repository

import (
	"context"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
)

// OutboxRepository defines the port for inserting domain events into the transactional outbox table (ADR-005).
type OutboxRepository interface {
	InsertOutboxEvent(ctx context.Context, evt *event.OutboxEvent) error
}
