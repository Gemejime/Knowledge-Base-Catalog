package chaos_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
)

type failingIngestionTrigger struct {
	err error
}

func (f *failingIngestionTrigger) TriggerBackfillDAG(_ context.Context, _ workflow.BackfillTriggerRequest) (string, error) {
	return "", f.err
}

func setupActiveKB(t *testing.T, repo *postgres.InMemoryCatalogRepo, cache *rediscache.InMemoryVersionedCache) *entity.KnowledgeBase {
	t.Helper()
	now := time.Now().UTC()
	kb := &entity.KnowledgeBase{
		TenantID:           "tenant-chaos",
		KBID:               "kb-chaos-001",
		Name:               "Chaos Test KB",
		Description:        "Testing Blue-Green failure resiliency",
		Version:            1,
		State:              entity.StateActive,
		RoutingPolicy:      entity.RoutingGreenOnly,
		ActiveIndexPointer: "idx_green_chaos_v1",
		ActiveSchema: entity.VectorSchema{
			EmbeddingModel: "text-embedding-3-small",
			Dimensions:     1536,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "v1.0",
		},
		RetrievalProfile: entity.RetrievalProfile{
			DefaultTopK:  10,
			DenseWeight:  0.7,
			SparseWeight: 0.3,
		},
		TaxonomyTags: []string{"env:test", "tier:gold"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventKBCreated, nil, now)
	if err != nil {
		t.Fatalf("failed to create outbox event: %v", err)
	}
	if err := repo.Create(context.Background(), kb, outboxEvt, nil); err != nil {
		t.Fatalf("failed to seed knowledge base: %v", err)
	}
	if cache != nil {
		_ = cache.PutVersion(context.Background(), kb)
	}
	return kb
}

// Scenario 1: Backfill job finishes but incoming live writes create a lag of 50 unprocessed chunks in replay buffer.
// Verify PromotePointer returns precondition failure (ErrDeltaLagNotZero).
func TestChaos_Scenario1_PromoteBlockedOnNonZeroDeltaLag(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	trigger := workflow.NewDeterministicIngestionTrigger()
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)
	promoteCmd := command.NewPromotePointerCommand(repo, cache)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "operator-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     3072,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v2.0",
	}

	migResp, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err != nil {
		t.Fatalf("TriggerReindexing failed: %v", err)
	}
	if migResp.State != entity.StateReindexing {
		t.Fatalf("expected state REINDEXING, got %s", migResp.State)
	}
	if migResp.RoutingPolicy != entity.RoutingDualWrite {
		t.Fatalf("expected routing DUAL_WRITE, got %s", migResp.RoutingPolicy)
	}

	// Ingestion backfill finished, but live write stream generated 50 unprocessed delta records
	if err := repo.UpdateMigrationProgress(ctx, kb.TenantID, kb.KBID, 50, true, true); err != nil {
		t.Fatalf("UpdateMigrationProgress failed: %v", err)
	}

	// Attempting to promote pointer must fail with ErrDeltaLagNotZero (HTTP 409 / gRPC FailedPrecondition)
	_, err = promoteCmd.Execute(ctx, dto.PromotePointerInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version,
	})
	if err == nil {
		t.Fatalf("expected promote pointer to fail with non-zero delta lag, got nil")
	}
	if !errors.Is(err, entity.ErrDeltaLagNotZero) {
		t.Fatalf("expected ErrDeltaLagNotZero, got: %v", err)
	}

	// Verify KB remains in REINDEXING state with candidate Blue intact and DUAL_WRITE active
	currentKB, err := repo.GetByID(ctx, kb.TenantID, kb.KBID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if currentKB.State != entity.StateReindexing {
		t.Errorf("expected state to remain REINDEXING, got: %s", currentKB.State)
	}
	if currentKB.RoutingPolicy != entity.RoutingDualWrite {
		t.Errorf("expected routing to remain DUAL_WRITE, got: %s", currentKB.RoutingPolicy)
	}
	if currentKB.CandidateIndexPointer != migResp.CandidateIndexPointer {
		t.Errorf("expected candidate pointer preserved, got: %s", currentKB.CandidateIndexPointer)
	}
}

// Scenario 2: Worker failure in rag-ingestion-coordinator. Trigger AbortReindexing;
// verify search pointer remains pinned to Green, state resets to ACTIVE, and cleanup event is dispatched to outbox to purge orphaned Blue index.
func TestChaos_Scenario2_IngestionWorkerCrashRollbackViaAbort(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	trigger := workflow.NewDeterministicIngestionTrigger()
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)
	abortCmd := command.NewAbortReindexingCommand(repo, cache)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "operator-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "bge-m3",
		Dimensions:     1024,
		DistanceMetric: entity.MetricDotProduct,
		SchemaVersion:  "v2.0",
	}

	migResp, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err != nil {
		t.Fatalf("TriggerReindexing failed: %v", err)
	}

	orphanedBlue := migResp.CandidateIndexPointer
	originalGreen := kb.ActiveIndexPointer

	// Worker crash: rag-ingestion-coordinator backfill task failed permanently.
	// Admin or health watchdog triggers AbortReindexing.
	abortReason := "rag-ingestion-coordinator node OOMKilled during chunk embedding"
	abortResp, err := abortCmd.Execute(ctx, dto.AbortReindexingInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version,
		Reason:          abortReason,
	})
	if err != nil {
		t.Fatalf("AbortReindexing failed: %v", err)
	}

	// Verify aggregate settled back to ACTIVE with Green pointer intact and candidate cleared
	if abortResp.KnowledgeBase.State != entity.StateActive {
		t.Errorf("expected state to settle at ACTIVE, got: %s", abortResp.KnowledgeBase.State)
	}
	if abortResp.KnowledgeBase.RoutingPolicy != entity.RoutingGreenOnly {
		t.Errorf("expected routing GREEN_ONLY, got: %s", abortResp.KnowledgeBase.RoutingPolicy)
	}
	if abortResp.KnowledgeBase.ActiveIndexPointer != originalGreen {
		t.Errorf("expected active pointer pinned to original Green %s, got: %s", originalGreen, abortResp.KnowledgeBase.ActiveIndexPointer)
	}
	if abortResp.KnowledgeBase.CandidateIndexPointer != "" {
		t.Errorf("expected candidate pointer cleared, got: %s", abortResp.KnowledgeBase.CandidateIndexPointer)
	}
	if abortResp.KnowledgeBase.CandidateSchema != nil {
		t.Errorf("expected candidate schema cleared, got: %v", abortResp.KnowledgeBase.CandidateSchema)
	}

	// Verify outbox event contains ReindexAborted with orphaned_blue_pointer to trigger external cleanup
	events := repo.OutboxEvents()
	var foundAbortEvent bool
	for _, e := range events {
		if e.EventType == event.EventReindexAborted {
			foundAbortEvent = true
			var payloadMap map[string]any
			if err := json.Unmarshal(e.Payload, &payloadMap); err != nil {
				t.Fatalf("failed to unmarshal outbox payload: %v", err)
			}
			if payloadMap["orphaned_blue_pointer"] != orphanedBlue {
				t.Errorf("expected orphaned_blue_pointer %s, got %v", orphanedBlue, payloadMap["orphaned_blue_pointer"])
			}
			if payloadMap["abort_reason"] != abortReason {
				t.Errorf("expected abort_reason %s, got %v", abortReason, payloadMap["abort_reason"])
			}
		}
	}
	if !foundAbortEvent {
		t.Fatalf("expected ReindexAborted event in outbox, none found")
	}
}

// Scenario 3: Concurrent OCC conflict during pointer promotion.
// Verify that if two concurrent requests attempt to promote or update the index pointer simultaneously,
// exactly one succeeds and the second returns ErrVersionConflict (HTTP 409 Conflict / gRPC Aborted).
func TestChaos_Scenario3_ConcurrentOCCConflictDuringPromotion(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	trigger := workflow.NewDeterministicIngestionTrigger()
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)
	promoteCmd := command.NewPromotePointerCommand(repo, cache)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "operator-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     3072,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v2.0",
	}

	migResp, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err != nil {
		t.Fatalf("TriggerReindexing failed: %v", err)
	}

	// Set migration ready for promotion: lag = 0, backfill = true, fence = true
	if err := repo.UpdateMigrationProgress(ctx, kb.TenantID, kb.KBID, 0, true, true); err != nil {
		t.Fatalf("UpdateMigrationProgress failed: %v", err)
	}

	const concurrentWorkers = 10
	var wg sync.WaitGroup
	wg.Add(concurrentWorkers)

	successCount := 0
	conflictCount := 0
	var mu sync.Mutex

	for i := 0; i < concurrentWorkers; i++ {
		go func() {
			defer wg.Done()
			_, promoteErr := promoteCmd.Execute(ctx, dto.PromotePointerInput{
				Caller:          caller,
				KBID:            kb.KBID,
				ExpectedVersion: migResp.Version,
			})
			mu.Lock()
			defer mu.Unlock()
			if promoteErr == nil {
				successCount++
			} else if errors.Is(promoteErr, entity.ErrVersionConflict) || errors.Is(promoteErr, entity.ErrInvalidStateTransition) {
				conflictCount++
			}
		}()
	}

	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 promotion success, got %d (conflicts: %d)", successCount, conflictCount)
	}
	if conflictCount != concurrentWorkers-1 {
		t.Fatalf("expected %d conflicts, got %d", concurrentWorkers-1, conflictCount)
	}

	// Verify post-promotion state: active pointer is Blue, state is ACTIVE, version incremented
	finalKB, err := repo.GetByID(ctx, kb.TenantID, kb.KBID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if finalKB.State != entity.StateActive {
		t.Errorf("expected final state ACTIVE, got: %s", finalKB.State)
	}
	if finalKB.RoutingPolicy != entity.RoutingGreenOnly {
		t.Errorf("expected routing GREEN_ONLY, got: %s", finalKB.RoutingPolicy)
	}
	if finalKB.ActiveIndexPointer != migResp.CandidateIndexPointer {
		t.Errorf("expected Blue candidate promoted to active pointer %s, got: %s", migResp.CandidateIndexPointer, finalKB.ActiveIndexPointer)
	}
	if finalKB.Version != migResp.Version+1 {
		t.Errorf("expected final version %d, got: %d", migResp.Version+1, finalKB.Version)
	}
}

// Scenario 4: Temporal DAG failure and rollback resiliency.
// If Temporal backfill workflow trigger fails, verify TriggerReindexing fails closed and
// automatically aborts the migration, keeping Green index intact and operational state ACTIVE.
func TestChaos_Scenario4_TemporalTriggerFailureRollback(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	failTrigger := &failingIngestionTrigger{err: errors.New("temporal cluster unreachable (503 Service Unavailable)")}
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, failTrigger)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "operator-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     3072,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v2.0",
	}

	_, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err == nil {
		t.Fatalf("expected TriggerReindexing to fail when Temporal trigger fails, got nil")
	}

	// Verify aggregate was automatically rolled back to ACTIVE with Green index intact
	revertedKB, err := repo.GetByID(ctx, kb.TenantID, kb.KBID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if revertedKB.State != entity.StateActive {
		t.Errorf("expected state to settle at ACTIVE after rollback, got: %s", revertedKB.State)
	}
	if revertedKB.RoutingPolicy != entity.RoutingGreenOnly {
		t.Errorf("expected routing policy GREEN_ONLY, got: %s", revertedKB.RoutingPolicy)
	}
	if revertedKB.CandidateIndexPointer != "" {
		t.Errorf("expected candidate pointer cleared, got: %s", revertedKB.CandidateIndexPointer)
	}
}

// Scenario 5: Write-fence drain gating: authoritative delta lag is 0 and backfill is completed,
// but write fence has NOT drained (write_fence_drained = false). Verify PromotePointer is blocked.
func TestChaos_Scenario5_WriteFenceNotDrainedBlocksPromotion(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	trigger := workflow.NewDeterministicIngestionTrigger()
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)
	promoteCmd := command.NewPromotePointerCommand(repo, cache)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "operator-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     3072,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v2.0",
	}

	migResp, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err != nil {
		t.Fatalf("TriggerReindexing failed: %v", err)
	}

	// Case 5A: delta_lag == 0, backfill_completed == true, BUT write_fence_drained == false
	if err := repo.UpdateMigrationProgress(ctx, kb.TenantID, kb.KBID, 0, true, false); err != nil {
		t.Fatalf("UpdateMigrationProgress failed: %v", err)
	}

	_, err = promoteCmd.Execute(ctx, dto.PromotePointerInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version,
	})
	if err == nil {
		t.Fatalf("expected promotion to be blocked when write fence is not drained, got nil")
	}
	if !errors.Is(err, entity.ErrDeltaLagNotZero) {
		t.Fatalf("expected ErrDeltaLagNotZero, got: %v", err)
	}

	// Case 5B: delta_lag == 0, write_fence_drained == true, BUT backfill_completed == false
	if err := repo.UpdateMigrationProgress(ctx, kb.TenantID, kb.KBID, 0, false, true); err != nil {
		t.Fatalf("UpdateMigrationProgress failed: %v", err)
	}

	_, err = promoteCmd.Execute(ctx, dto.PromotePointerInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version,
	})
	if err == nil {
		t.Fatalf("expected promotion to be blocked when backfill is not completed, got nil")
	}
	if !errors.Is(err, entity.ErrDeltaLagNotZero) {
		t.Fatalf("expected ErrDeltaLagNotZero, got: %v", err)
	}

	// Case 5C: All gates cleared (lag == 0, backfill == true, fence == true) -> promotion succeeds!
	if err := repo.UpdateMigrationProgress(ctx, kb.TenantID, kb.KBID, 0, true, true); err != nil {
		t.Fatalf("UpdateMigrationProgress failed: %v", err)
	}

	promotedResp, err := promoteCmd.Execute(ctx, dto.PromotePointerInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version,
	})
	if err != nil {
		t.Fatalf("expected promotion to succeed when all gates cleared, got: %v", err)
	}
	if promotedResp.KnowledgeBase.State != entity.StateActive {
		t.Errorf("expected state ACTIVE, got: %s", promotedResp.KnowledgeBase.State)
	}
	if promotedResp.KnowledgeBase.ActiveIndexPointer != migResp.CandidateIndexPointer {
		t.Errorf("expected Blue promoted, got: %s", promotedResp.KnowledgeBase.ActiveIndexPointer)
	}
}

// Scenario 6: Direct REINDEXING -> ACTIVE transition is strictly disallowed.
func TestChaos_Scenario6_DirectReindexingToActiveForbidden(t *testing.T) {
	err := entity.ValidateTransition(entity.StateReindexing, entity.StateActive, 0)
	if err == nil {
		t.Fatalf("expected direct REINDEXING -> ACTIVE transition to be disallowed, got nil")
	}
	if !errors.Is(err, entity.ErrInvalidStateTransition) {
		t.Errorf("expected ErrInvalidStateTransition, got: %v", err)
	}
}
