package chaos_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
)

var errInjectedChaosFailure = errors.New("injected database execution chaos failure")

// FailingCatalogRepo wraps an underlying CatalogRepository to inject transaction-time failures,
// verifying rollback atomicity and ensuring zero orphaned outbox events are committed (ADR-005).
type FailingCatalogRepo struct {
	repository.CatalogRepository
	failUpdate   atomic.Bool
	failCreation atomic.Bool
}

func (f *FailingCatalogRepo) UpdateMetadata(ctx context.Context, kb *entity.KnowledgeBase, expectedVersion uint64, outbox *event.OutboxEvent) error {
	if f.failUpdate.Load() {
		return errInjectedChaosFailure
	}
	return f.CatalogRepository.UpdateMetadata(ctx, kb, expectedVersion, outbox)
}

func (f *FailingCatalogRepo) Create(ctx context.Context, kb *entity.KnowledgeBase, outbox *event.OutboxEvent, idempotency *repository.IdempotencyRecord) error {
	if f.failCreation.Load() {
		return errInjectedChaosFailure
	}
	return f.CatalogRepository.Create(ctx, kb, outbox, idempotency)
}

// TestChaos_TransactionalRollbackAtomicity asserts that when an entity mutation encounters
// an execution failure or context cancellation, zero orphaned outbox records are appended to the outbox stream.
func TestChaos_TransactionalRollbackAtomicity(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	baseRepo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	failingRepo := &FailingCatalogRepo{CatalogRepository: baseRepo}

	// 1. Injected failure during creation: zero records committed
	failingRepo.failCreation.Store(true)
	createCmd := command.NewCreateKnowledgeBaseCommand(failingRepo, cache)
	caller := policy.CallerIdentity{
		TenantID: "tenant-chaos-atomic",
		UserID:   "admin-1",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	_, err := createCmd.Execute(ctx, dto.CreateKBInput{
		Caller:      caller,
		Name:        "Atomic Create Test",
		Description: "Should rollback cleanly",
		VectorSchema: entity.VectorSchema{
			EmbeddingModel: "text-embedding-3-small",
			Dimensions:     1536,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "v1.0",
		},
		RetrievalProfile: entity.RetrievalProfile{
			DefaultTopK:               10,
			DenseWeight:               0.7,
			SparseWeight:              0.3,
			RerankerCandidatePoolSize: 50,
		},
	})
	if !errors.Is(err, errInjectedChaosFailure) {
		t.Fatalf("expected injected failure, got: %v", err)
	}

	if len(baseRepo.OutboxEvents()) != 0 {
		t.Fatalf("expected 0 outbox events after aborted creation, got %d", len(baseRepo.OutboxEvents()))
	}

	// 2. Allow successful creation: exactly 1 outbox event (KBCreated)
	failingRepo.failCreation.Store(false)
	createdKB, err := createCmd.Execute(ctx, dto.CreateKBInput{
		Caller:      caller,
		Name:        "Atomic Create Success",
		Description: "Should succeed",
		VectorSchema: entity.VectorSchema{
			EmbeddingModel: "text-embedding-3-small",
			Dimensions:     1536,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "v1.0",
		},
		RetrievalProfile: entity.RetrievalProfile{
			DefaultTopK:               10,
			DenseWeight:               0.7,
			SparseWeight:              0.3,
			RerankerCandidatePoolSize: 50,
		},
	})
	if err != nil {
		t.Fatalf("unexpected creation failure: %v", err)
	}

	if len(baseRepo.OutboxEvents()) != 1 {
		t.Fatalf("expected exactly 1 outbox event, got %d", len(baseRepo.OutboxEvents()))
	}
	if baseRepo.OutboxEvents()[0].EventType != event.EventKBCreated {
		t.Fatalf("expected KBCreated event, got %s", baseRepo.OutboxEvents()[0].EventType)
	}

	// 3. Inject failure during Update: update fails and outbox stream remains unchanged
	failingRepo.failUpdate.Store(true)
	updateCmd := command.NewUpdateKnowledgeBaseCommand(failingRepo, cache)
	newName := "Attempted Failed Update"
	_, err = updateCmd.Execute(ctx, dto.UpdateKBInput{
		Caller:          caller,
		KBID:            createdKB.KnowledgeBase.KBID,
		ExpectedVersion: 1,
		Name:            &newName,
	})
	if !errors.Is(err, errInjectedChaosFailure) {
		t.Fatalf("expected injected update failure, got: %v", err)
	}

	// Assert outbox count remains exactly 1 — no orphaned KBUpdated event written
	if len(baseRepo.OutboxEvents()) != 1 {
		t.Fatalf("expected exactly 1 outbox event after failed update rollback, got %d", len(baseRepo.OutboxEvents()))
	}

	// Assert entity version in repository was not incremented
	kbInRepo, err := baseRepo.GetByID(ctx, caller.TenantID, createdKB.KnowledgeBase.KBID)
	if err != nil {
		t.Fatalf("failed to retrieve KB: %v", err)
	}
	if kbInRepo.Version != 1 {
		t.Fatalf("expected version to remain 1 after rollback, got %d", kbInRepo.Version)
	}
}

// TestChaos_ConcurrentOCCConflictStormUnderLoad simulates 20 concurrent goroutines racing to update
// the same Knowledge Base entity under Optimistic Concurrency Control (OCC).
// Asserts that exactly 1 update succeeds (version + 1, exactly 1 outbox event) and exactly 19 fail with ErrVersionConflict.
func TestChaos_ConcurrentOCCConflictStormUnderLoad(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	initialOutboxCount := len(repo.OutboxEvents())
	if initialOutboxCount != 1 {
		t.Fatalf("expected 1 initial outbox event (KBCreated), got %d", initialOutboxCount)
	}

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "concurrent-worker",
		Roles:    []policy.Role{policy.RoleEditor},
	}

	updateCmd := command.NewUpdateKnowledgeBaseCommand(repo, cache)

	const concurrency = 20
	var successCount int64
	var conflictCount int64
	var wg sync.WaitGroup
	wg.Add(concurrency)

	startBarrier := make(chan struct{})

	for i := 0; i < concurrency; i++ {
		go func(workerID int) {
			defer wg.Done()
			<-startBarrier

			desc := "Concurrent update from worker"
			_, err := updateCmd.Execute(ctx, dto.UpdateKBInput{
				Caller:          caller,
				KBID:            kb.KBID,
				ExpectedVersion: kb.Version, // All race with version = 1
				Description:     &desc,
			})
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else if errors.Is(err, entity.ErrVersionConflict) {
				atomic.AddInt64(&conflictCount, 1)
			} else {
				t.Errorf("worker %d unexpected error: %v", workerID, err)
			}
		}(i)
	}

	// Release all 20 goroutines simultaneously
	close(startBarrier)
	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful OCC update, got %d", successCount)
	}
	if conflictCount != int64(concurrency-1) {
		t.Fatalf("expected exactly %d OCC conflicts, got %d", concurrency-1, conflictCount)
	}

	// Verify final entity state
	updatedKB, err := repo.GetByID(ctx, kb.TenantID, kb.KBID)
	if err != nil {
		t.Fatalf("failed to get updated KB: %v", err)
	}
	if updatedKB.Version != 2 {
		t.Fatalf("expected version to be exactly 2, got %d", updatedKB.Version)
	}

	// Verify outbox stream contains exactly 2 events (1 KBCreated + 1 KBUpdated)
	finalOutbox := repo.OutboxEvents()
	if len(finalOutbox) != 2 {
		t.Fatalf("expected exactly 2 outbox events, got %d", len(finalOutbox))
	}
	if finalOutbox[1].EventType != event.EventKBUpdated {
		t.Fatalf("expected second outbox event to be KBUpdated, got %s", finalOutbox[1].EventType)
	}
	if finalOutbox[1].AggregateVersion != 2 {
		t.Fatalf("expected outbox event version 2, got %d", finalOutbox[1].AggregateVersion)
	}
}

// TestChaos_SoftDeleteOutboxAtomicity validates that deleting an active knowledge base
// undergoing reindexing atomically marks the migration ABORTED, records candidate index pointer
// in the KBDeleted outbox event for cleanup, and decrements active migration gauges.
func TestChaos_SoftDeleteOutboxAtomicity(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	kb := setupActiveKB(t, repo, cache)

	caller := policy.CallerIdentity{
		TenantID: kb.TenantID,
		UserID:   "admin-delete",
		Roles:    []policy.Role{policy.RoleOwner},
	}

	trigger := workflow.NewDeterministicIngestionTrigger()
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)

	targetSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     3072,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v2.0",
	}

	// Initiate reindexing
	migResp, err := triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             kb.KBID,
		ExpectedVersion:  kb.Version,
		TargetBlueSchema: targetSchema,
	})
	if err != nil {
		t.Fatalf("unexpected error initiating reindexing: %v", err)
	}

	// Verify active migration exists
	activeMig, err := repo.GetActiveMigration(ctx, kb.TenantID, kb.KBID)
	if err != nil || activeMig == nil {
		t.Fatalf("expected active migration in REINDEXING state, got: %v", err)
	}

	// Execute Soft Delete
	deleteCmd := command.NewDeleteKnowledgeBaseCommand(repo, cache)
	err = deleteCmd.Execute(ctx, dto.DeleteKBInput{
		Caller:          caller,
		KBID:            kb.KBID,
		ExpectedVersion: migResp.Version, // Expected version after reindexing initiation (v2)
	})
	if err != nil {
		t.Fatalf("unexpected delete error: %v", err)
	}

	// 1. Assert entity is marked TOMBSTONED: GetByID hides tombstoned entities returning ErrKnowledgeBaseNotFound
	_, err = repo.GetByID(ctx, kb.TenantID, kb.KBID)
	if !errors.Is(err, entity.ErrKnowledgeBaseNotFound) {
		t.Fatalf("expected ErrKnowledgeBaseNotFound for soft-deleted entity, got: %v", err)
	}

	// 2. Assert active migration is cleaned up from active map
	_, err = repo.GetActiveMigration(ctx, kb.TenantID, kb.KBID)
	if !errors.Is(err, entity.ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition after soft delete, got: %v", err)
	}

	// 3. Assert outbox events: KBCreated (v1), ReindexInitiated (v2), KBDeleted (v3)
	outbox := repo.OutboxEvents()
	if len(outbox) != 3 {
		t.Fatalf("expected 3 outbox events, got %d", len(outbox))
	}

	deleteEvt := outbox[2]
	if deleteEvt.EventType != event.EventKBDeleted {
		t.Fatalf("expected EventKBDeleted, got %s", deleteEvt.EventType)
	}
	if deleteEvt.AggregateVersion != 3 {
		t.Fatalf("expected aggregate version 3 in outbox, got %d", deleteEvt.AggregateVersion)
	}

	var payload map[string]any
	if err := json.Unmarshal(deleteEvt.Payload, &payload); err != nil {
		t.Fatalf("failed to parse delete payload: %v", err)
	}

	if payload["candidate_index_pointer"] != migResp.CandidateIndexPointer {
		t.Fatalf("expected candidate_index_pointer %q in outbox for downstream cleanup, got %v", migResp.CandidateIndexPointer, payload["candidate_index_pointer"])
	}
	if payload["active_migrations_cancelled"] != float64(1) {
		t.Fatalf("expected active_migrations_cancelled = 1, got %v", payload["active_migrations_cancelled"])
	}
}
