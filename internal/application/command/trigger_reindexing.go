package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

// TriggerReindexingCommand transitions a Knowledge Base from ACTIVE to REINDEXING, enables DUAL_WRITE routing,
// commits ReindexInitiated to the transactional outbox, and triggers the backfill workflow in rag-ingestion-coordinator.
type TriggerReindexingCommand struct {
	repo    repository.CatalogRepository
	cache   repository.VersionedCache
	trigger workflow.IngestionTrigger
}

// NewTriggerReindexingCommand constructs a TriggerReindexingCommand.
func NewTriggerReindexingCommand(
	repo repository.CatalogRepository,
	cache repository.VersionedCache,
	trigger workflow.IngestionTrigger,
) *TriggerReindexingCommand {
	if trigger == nil {
		trigger = workflow.NewNoopIngestionTrigger()
	}
	return &TriggerReindexingCommand{
		repo:    repo,
		cache:   cache,
		trigger: trigger,
	}
}

// Execute initiates the Blue-Green migration state machine under OCC.
func (c *TriggerReindexingCommand) Execute(ctx context.Context, in dto.TriggerReindexingInput) (*dto.MigrationResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionTriggerReindexing); err != nil {
		return nil, err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return nil, fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}
	if in.ExpectedVersion == 0 {
		return nil, fmt.Errorf("%w: expected_version must be >= 1", entity.ErrInvalidInput)
	}

	kb, err := c.repo.GetByID(ctx, in.Caller.TenantID, kbID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	migration, err := kb.InitiateReindexing("", in.TargetBlueSchema, in.ExpectedVersion, now)
	if err != nil {
		return nil, err
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventReindexInitiated, map[string]any{
		"migration_id":         migration.MigrationID,
		"source_green_pointer": migration.SourceGreenPointer,
		"target_blue_pointer":  migration.TargetBluePointer,
		"temporal_workflow_id": migration.TemporalWorkflowID,
	}, now)
	if err != nil {
		return nil, err
	}

	if err := c.repo.TriggerReindexing(ctx, kb, migration, in.ExpectedVersion, outboxEvt); err != nil {
		return nil, err
	}

	if c.cache != nil {
		_ = c.cache.PutVersion(ctx, kb)
	}

	if err := c.dispatchBackfillWithRetry(ctx, workflow.BackfillTriggerRequest{
		TenantID:           kb.TenantID,
		KBID:               kb.KBID,
		MigrationID:        migration.MigrationID,
		WorkflowID:         migration.TemporalWorkflowID,
		SourceGreenPointer: migration.SourceGreenPointer,
		TargetBluePointer:  migration.TargetBluePointer,
		TargetSchema:       migration.TargetSchema,
	}); err != nil {
		abortNow := time.Now().UTC()
		if abortedKB, abortErr := c.repo.AbortReindexing(ctx, kb.TenantID, kb.KBID, kb.Version, "backfill workflow trigger failed", abortNow); abortErr == nil && c.cache != nil && abortedKB != nil {
			_ = c.cache.PutVersion(ctx, abortedKB)
		}
		return nil, fmt.Errorf("failed to trigger ingestion backfill workflow: %w", err)
	}

	return &dto.MigrationResponse{
		TenantID:              kb.TenantID,
		KBID:                  kb.KBID,
		MigrationID:           migration.MigrationID,
		State:                 kb.State,
		RoutingPolicy:         kb.RoutingPolicy,
		CandidateIndexPointer: kb.CandidateIndexPointer,
		Version:               kb.Version,
	}, nil
}

func (c *TriggerReindexingCommand) dispatchBackfillWithRetry(ctx context.Context, req workflow.BackfillTriggerRequest) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := c.trigger.TriggerBackfillDAG(ctx, req); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return lastErr
}
