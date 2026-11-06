package entity

import (
	"fmt"
	"strings"
	"time"
)

// KBMigration represents an authoritative Blue-Green migration record in kb_migrations.
type KBMigration struct {
	TenantID           string         `json:"tenant_id"`
	MigrationID        string         `json:"migration_id"`
	KBID               string         `json:"kb_id"`
	SourceGreenPointer string         `json:"source_green_pointer"`
	TargetBluePointer  string         `json:"target_blue_pointer"`
	SourceSchema       VectorSchema   `json:"source_schema"`
	TargetSchema       VectorSchema   `json:"target_schema"`
	State              LifecycleState `json:"state"`
	DeltaLagRecords    uint64         `json:"delta_lag_records"`
	BackfillCompleted  bool           `json:"backfill_completed"`
	WriteFenceDrained  bool           `json:"write_fence_drained"`
	TemporalWorkflowID string         `json:"temporal_workflow_id"`
	AbortReason        string         `json:"abort_reason,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// ValidateTransition enforces the Blue-Green migration state machine (ADR-001 & ADR-002):
//   - CREATING   -> ACTIVE     (Allowed)
//   - ACTIVE     -> REINDEXING (Allowed)
//   - REINDEXING -> PROMOTED   (Allowed ONLY IF authoritativeDeltaLag == 0)
//   - REINDEXING -> ABORTED    (Allowed)
//   - REINDEXING -> ACTIVE     (Strictly Disallowed without explicit PROMOTED or ABORTED transition)
//   - ACTIVE / REINDEXING -> TOMBSTONED (Allowed for soft-deletion)
func ValidateTransition(from, to LifecycleState, authoritativeDeltaLag uint64) error {
	if !from.IsValid() || !to.IsValid() {
		return fmt.Errorf("%w: unrecognized transition %q -> %q", ErrInvalidStateTransition, from, to)
	}
	if from == StateTombstoned {
		return fmt.Errorf("%w: cannot transition from terminal state TOMBSTONED", ErrInvalidStateTransition)
	}

	switch from {
	case StateCreating:
		if to == StateActive {
			return nil
		}
	case StateActive:
		if to == StateReindexing || to == StateTombstoned {
			return nil
		}
	case StateReindexing:
		switch to {
		case StatePromoted:
			if authoritativeDeltaLag > 0 {
				return fmt.Errorf("%w: current authoritative delta lag is %d records", ErrDeltaLagNotZero, authoritativeDeltaLag)
			}
			return nil
		case StateAborted, StateTombstoned:
			return nil
		case StateActive:
			return fmt.Errorf("%w: direct REINDEXING -> ACTIVE transition is disallowed; must execute PromoteIndexPointer or AbortReindexing", ErrInvalidStateTransition)
		}
	case StatePromoted, StateAborted:
		if to == StateActive {
			return nil
		}
	}

	return fmt.Errorf("%w: transition from %s to %s is not permitted", ErrInvalidStateTransition, from, to)
}

// InitiateReindexing transitions the KnowledgeBase from ACTIVE to REINDEXING, enables DUAL_WRITE,
// allocates a candidate Blue index pointer, and produces a tracked KBMigration record.
func (kb *KnowledgeBase) InitiateReindexing(
	migrationID string,
	targetBlueSchema VectorSchema,
	expectedVersion uint64,
	now time.Time,
) (*KBMigration, error) {
	if kb.State == StateTombstoned {
		return nil, ErrKnowledgeBaseNotFound
	}
	if err := ValidateTransition(kb.State, StateReindexing, 0); err != nil {
		return nil, err
	}
	if kb.Version != expectedVersion {
		return nil, fmt.Errorf("%w: expected version %d, actual %d", ErrVersionConflict, expectedVersion, kb.Version)
	}
	if err := DefaultModelRegistry().ValidateSchema(targetBlueSchema); err != nil {
		return nil, err
	}
	if migrationID == "" {
		var err error
		migrationID, err = NewUUIDv4()
		if err != nil {
			return nil, err
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	nextVersion := kb.Version + 1
	bluePointer := FormatBluePointer(kb.TenantID, kb.KBID, nextVersion)
	targetCopy := targetBlueSchema

	kb.State = StateReindexing
	kb.RoutingPolicy = RoutingDualWrite
	kb.CandidateIndexPointer = bluePointer
	kb.CandidateSchema = &targetCopy
	kb.Version = nextVersion
	kb.UpdatedAt = now

	migration := &KBMigration{
		TenantID:           kb.TenantID,
		MigrationID:        migrationID,
		KBID:               kb.KBID,
		SourceGreenPointer: kb.ActiveIndexPointer,
		TargetBluePointer:  bluePointer,
		SourceSchema:       kb.ActiveSchema,
		TargetSchema:       targetBlueSchema,
		State:              StateReindexing,
		DeltaLagRecords:    0,
		TemporalWorkflowID: fmt.Sprintf("reindex-%s-%s", kb.KBID, migrationID),
		AbortReason:        "",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	return migration, nil
}

// PromotePointer validates zero authoritative delta lag (and optional client lag assertion),
// transitions REINDEXING -> PROMOTED, switches the active pointer and schema to Blue,
// resets routing_policy to GREEN_ONLY, and settles operational state at ACTIVE.
func (kb *KnowledgeBase) PromotePointer(
	migration *KBMigration,
	expectedVersion uint64,
	authoritativeDeltaLag uint64,
	clientVerifiedLag *uint64,
	now time.Time,
) error {
	if kb.State == StateTombstoned {
		return ErrKnowledgeBaseNotFound
	}
	if kb.State != StateReindexing {
		return fmt.Errorf("%w: knowledge base must be in REINDEXING state to promote pointer (current: %s)", ErrInvalidStateTransition, kb.State)
	}
	if kb.Version != expectedVersion {
		return fmt.Errorf("%w: expected version %d, actual %d", ErrVersionConflict, expectedVersion, kb.Version)
	}
	if clientVerifiedLag != nil && *clientVerifiedLag > 0 {
		return fmt.Errorf("%w: client reported non-zero delta lag (%d)", ErrDeltaLagNotZero, *clientVerifiedLag)
	}
	if err := ValidateTransition(kb.State, StatePromoted, authoritativeDeltaLag); err != nil {
		return err
	}
	if migration != nil && (!migration.BackfillCompleted || !migration.WriteFenceDrained) {
		return fmt.Errorf("%w: backfill completion (%t) and drained write fence (%t) are required before pointer promotion",
			ErrDeltaLagNotZero, migration.BackfillCompleted, migration.WriteFenceDrained)
	}
	if kb.CandidateIndexPointer == "" || kb.CandidateSchema == nil {
		return fmt.Errorf("%w: missing candidate blue index pointer or schema", ErrInvalidStateTransition)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	// Step 1: Transition through PROMOTED.
	kb.State = StatePromoted
	if migration != nil {
		migration.State = StatePromoted
		migration.DeltaLagRecords = 0
		migration.UpdatedAt = now
	}

	// Step 2: Atomically switch Green -> Blue and settle operational state at ACTIVE.
	kb.ActiveIndexPointer = kb.CandidateIndexPointer
	kb.ActiveSchema = *kb.CandidateSchema
	kb.CandidateIndexPointer = ""
	kb.CandidateSchema = nil
	kb.RoutingPolicy = RoutingGreenOnly
	kb.State = StateActive
	kb.Version++
	kb.UpdatedAt = now
	return nil
}

// AbortReindexing transitions REINDEXING -> ABORTED, keeps the Green pointer locked,
// clears the Blue candidate pointer, resets routing_policy to GREEN_ONLY, and settles operational state at ACTIVE.
func (kb *KnowledgeBase) AbortReindexing(
	migration *KBMigration,
	expectedVersion uint64,
	reason string,
	now time.Time,
) (orphanedBluePointer string, err error) {
	if kb.State == StateTombstoned {
		return "", ErrKnowledgeBaseNotFound
	}
	if kb.State != StateReindexing {
		return "", fmt.Errorf("%w: knowledge base must be in REINDEXING state to abort (current: %s)", ErrInvalidStateTransition, kb.State)
	}
	if kb.Version != expectedVersion {
		return "", fmt.Errorf("%w: expected version %d, actual %d", ErrVersionConflict, expectedVersion, kb.Version)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("%w: abort reason is required", ErrInvalidInput)
	}
	if err := ValidateTransition(kb.State, StateAborted, 0); err != nil {
		return "", err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	orphanedBluePointer = kb.CandidateIndexPointer
	kb.State = StateAborted
	if migration != nil {
		migration.State = StateAborted
		migration.AbortReason = reason
		migration.UpdatedAt = now
	}

	// Settle aggregate back to ACTIVE with Green pointer intact.
	kb.CandidateIndexPointer = ""
	kb.CandidateSchema = nil
	kb.RoutingPolicy = RoutingGreenOnly
	kb.State = StateActive
	kb.Version++
	kb.UpdatedAt = now
	return orphanedBluePointer, nil
}

// MarkTombstoned transitions an ACTIVE or REINDEXING Knowledge Base to TOMBSTONED under OCC.
func (kb *KnowledgeBase) MarkTombstoned(expectedVersion uint64, now time.Time) error {
	if kb.State == StateTombstoned {
		return ErrKnowledgeBaseNotFound
	}
	if err := ValidateTransition(kb.State, StateTombstoned, 0); err != nil {
		return err
	}
	if kb.Version != expectedVersion {
		return fmt.Errorf("%w: expected version %d, actual %d", ErrVersionConflict, expectedVersion, kb.Version)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	kb.State = StateTombstoned
	kb.RoutingPolicy = RoutingGreenOnly
	kb.CandidateIndexPointer = ""
	kb.CandidateSchema = nil
	kb.Version++
	kb.UpdatedAt = now
	return nil
}
