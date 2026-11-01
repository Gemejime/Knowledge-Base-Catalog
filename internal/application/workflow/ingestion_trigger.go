package workflow

import (
	"context"
	"fmt"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// BackfillTriggerRequest encapsulates the parameters passed to rag-ingestion-coordinator via Temporal.
type BackfillTriggerRequest struct {
	TenantID           string
	KBID               string
	MigrationID        string
	WorkflowID         string
	SourceGreenPointer string
	TargetBluePointer  string
	TargetSchema       entity.VectorSchema
}

// IngestionTrigger defines the port for scheduling asynchronous Blue-Green backfill DAGs in rag-ingestion-coordinator.
type IngestionTrigger interface {
	TriggerBackfillDAG(ctx context.Context, req BackfillTriggerRequest) (workflowRunID string, err error)
}

// NoopIngestionTrigger provides a deterministic Phase 1 adapter prior to Phase 3's Temporal SDK integration.
type NoopIngestionTrigger struct{}

// NewNoopIngestionTrigger creates a safe Phase 1 no-op workflow trigger adapter.
func NewNoopIngestionTrigger() *NoopIngestionTrigger {
	return &NoopIngestionTrigger{}
}

// TriggerBackfillDAG returns a deterministic workflow run identifier without external network I/O.
func (n *NoopIngestionTrigger) TriggerBackfillDAG(_ context.Context, req BackfillTriggerRequest) (string, error) {
	if req.WorkflowID != "" {
		return req.WorkflowID, nil
	}
	return fmt.Sprintf("reindex-%s-%s", req.KBID, req.MigrationID), nil
}
