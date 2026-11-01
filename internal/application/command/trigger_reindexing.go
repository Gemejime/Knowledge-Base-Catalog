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

	workflowID, err := c.trigger.TriggerBackfillDAG(ctx, workflow.BackfillTriggerRequest{
		TenantID:           kb.TenantID,
		KBID:               kb.KBID,
		MigrationID:        migration.MigrationID,
		WorkflowID:         migration.TemporalWorkflowID,
		SourceGreenPointer: migration.SourceGreenPointer,
		TargetBluePointer:  migration.TargetBluePointer,
		TargetSchema:       migration.TargetSchema,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to trigger ingestion backfill workflow: %w", err)
	}
	migration.TemporalWorkflowID = workflowID

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
