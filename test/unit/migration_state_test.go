package unit_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

func TestBlueGreenMigrationStateMachine(t *testing.T) {
	t.Run("CREATING -> ACTIVE is allowed", func(t *testing.T) {
		if err := entity.ValidateTransition(entity.StateCreating, entity.StateActive, 0); err != nil {
			t.Fatalf("expected CREATING -> ACTIVE to succeed, got %v", err)
		}
	})

	t.Run("ACTIVE -> REINDEXING is allowed", func(t *testing.T) {
		if err := entity.ValidateTransition(entity.StateActive, entity.StateReindexing, 0); err != nil {
			t.Fatalf("expected ACTIVE -> REINDEXING to succeed, got %v", err)
		}
	})

	t.Run("REINDEXING -> PROMOTED is allowed when authoritative delta lag == 0", func(t *testing.T) {
		if err := entity.ValidateTransition(entity.StateReindexing, entity.StatePromoted, 0); err != nil {
			t.Fatalf("expected REINDEXING -> PROMOTED with lag=0 to succeed, got %v", err)
		}
	})

	t.Run("REINDEXING -> PROMOTED is blocked when authoritative delta lag > 0", func(t *testing.T) {
		err := entity.ValidateTransition(entity.StateReindexing, entity.StatePromoted, 50)
		if !errors.Is(err, entity.ErrDeltaLagNotZero) {
			t.Fatalf("expected ErrDeltaLagNotZero when lag=50, got %v", err)
		}
	})

	t.Run("REINDEXING -> ABORTED is allowed", func(t *testing.T) {
		if err := entity.ValidateTransition(entity.StateReindexing, entity.StateAborted, 100); err != nil {
			t.Fatalf("expected REINDEXING -> ABORTED to succeed even with lag>0, got %v", err)
		}
	})

	t.Run("direct REINDEXING -> ACTIVE is strictly disallowed", func(t *testing.T) {
		err := entity.ValidateTransition(entity.StateReindexing, entity.StateActive, 0)
		if !errors.Is(err, entity.ErrInvalidStateTransition) {
			t.Fatalf("expected ErrInvalidStateTransition on direct REINDEXING -> ACTIVE, got %v", err)
		}
	})
}

func TestKnowledgeBaseAggregateBlueGreenLifecycle(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	greenSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-small",
		Dimensions:     768,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v1",
	}
	blueSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     1536,
		DistanceMetric: entity.MetricDotProduct,
		SchemaVersion:  "v2",
	}
	profile := entity.RetrievalProfile{
		DefaultTopK:                     15,
		DenseWeight:                     0.7,
		SparseWeight:                    0.3,
		RerankerCandidatePoolSize:       30,
		CrossEncoderConfidenceThreshold: 0.65,
	}

	kb, err := entity.NewKnowledgeBase("tenant-alpha", "", "finance-corpus", "Initial KB", greenSchema, profile, []string{"env:production"}, now)
	if err != nil {
		t.Fatalf("NewKnowledgeBase failed: %v", err)
	}
	if kb.State != entity.StateActive || kb.RoutingPolicy != entity.RoutingGreenOnly || kb.Version != 1 {
		t.Fatalf("unexpected initial KB state: %+v", kb)
	}
	initialGreenPointer := kb.ActiveIndexPointer

	// 1. Trigger Reindexing (ACTIVE -> REINDEXING, DUAL_WRITE)
	mig, err := kb.InitiateReindexing("", blueSchema, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("InitiateReindexing failed: %v", err)
	}
	if kb.State != entity.StateReindexing || kb.RoutingPolicy != entity.RoutingDualWrite || kb.Version != 2 {
		t.Fatalf("expected REINDEXING + DUAL_WRITE + version 2, got state=%s policy=%s ver=%d", kb.State, kb.RoutingPolicy, kb.Version)
	}
	if kb.CandidateIndexPointer == "" || kb.ActiveIndexPointer != initialGreenPointer {
		t.Fatalf("expected active Green pointer preserved and candidate Blue pointer allocated")
	}

	// 2. Attempt PromotePointer with authoritative lag = 50 -> blocked with ErrDeltaLagNotZero
	mig.BackfillCompleted = true
	mig.WriteFenceDrained = true
	err = kb.PromotePointer(mig, 2, 50, nil, now.Add(2*time.Minute))
	if !errors.Is(err, entity.ErrDeltaLagNotZero) {
		t.Fatalf("expected ErrDeltaLagNotZero when authoritative lag=50, got %v", err)
	}

	// 2b. Attempt PromotePointer with zero lag but incomplete backfill/fence -> blocked
	mig.BackfillCompleted = false
	zeroLagVal := uint64(0)
	err = kb.PromotePointer(mig, 2, 0, &zeroLagVal, now.Add(2*time.Minute))
	if !errors.Is(err, entity.ErrDeltaLagNotZero) {
		t.Fatalf("expected ErrDeltaLagNotZero when backfill incomplete, got %v", err)
	}
	mig.BackfillCompleted = true
	mig.WriteFenceDrained = true

	// 3. PromotePointer with authoritative lag = 0, backfill completed, write fence drained -> switches to Blue pointer and settles at ACTIVE
	zeroLag := uint64(0)
	if err := kb.PromotePointer(mig, 2, 0, &zeroLag, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("PromotePointer with lag=0 failed: %v", err)
	}
	if kb.State != entity.StateActive || kb.RoutingPolicy != entity.RoutingGreenOnly || kb.Version != 3 {
		t.Fatalf("expected settled ACTIVE + GREEN_ONLY + version 3 after promotion, got %+v", kb)
	}
	if kb.ActiveIndexPointer != mig.TargetBluePointer || !kb.ActiveSchema.Equals(blueSchema) {
		t.Fatalf("expected active pointer and schema switched to Blue")
	}
	if mig.State != entity.StatePromoted {
		t.Fatalf("expected migration state PROMOTED, got %s", mig.State)
	}
}
