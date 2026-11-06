package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// ErrIngestionSchedulerNotConfigured indicates that no Temporal DAG scheduler is configured for backfill orchestration.
var ErrIngestionSchedulerNotConfigured = errors.New("ingestion backfill DAG scheduler is not configured")

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

// NoopIngestionTrigger provides a fail-closed adapter when no external Temporal DAG scheduler is configured.
type NoopIngestionTrigger struct{}

// NewNoopIngestionTrigger creates an unconfigured workflow trigger adapter that fails closed on TriggerBackfillDAG.
func NewNoopIngestionTrigger() *NoopIngestionTrigger {
	return &NoopIngestionTrigger{}
}

// TriggerBackfillDAG returns an error when no DAG scheduler is configured so TriggerReindexing does not report success for an unscheduled backfill.
func (n *NoopIngestionTrigger) TriggerBackfillDAG(_ context.Context, _ BackfillTriggerRequest) (string, error) {
	return "", ErrIngestionSchedulerNotConfigured
}

// DeterministicIngestionTrigger provides a deterministic in-memory workflow trigger adapter for standalone/test environments.
type DeterministicIngestionTrigger struct{}

// NewDeterministicIngestionTrigger creates a deterministic workflow trigger adapter.
func NewDeterministicIngestionTrigger() *DeterministicIngestionTrigger {
	return &DeterministicIngestionTrigger{}
}

// TriggerBackfillDAG returns a deterministic workflow run identifier without external network I/O.
func (d *DeterministicIngestionTrigger) TriggerBackfillDAG(_ context.Context, req BackfillTriggerRequest) (string, error) {
	if req.WorkflowID != "" {
		return req.WorkflowID, nil
	}
	return fmt.Sprintf("reindex-%s-%s", req.KBID, req.MigrationID), nil
}
