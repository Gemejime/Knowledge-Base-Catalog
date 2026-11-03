package unit_test

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
)

var uuidv4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// DebeziumOutboxHeaderProjection represents the expected Kafka header structure projected by
// Debezium Outbox EventRouter SMT (transforms.outbox.table.fields.additional.placement).
type DebeziumOutboxHeaderProjection struct {
	EventType        string
	AggregateVersion uint64
	TenantID         string
}

func simulateDebeziumHeaderExtraction(evt *event.OutboxEvent) DebeziumOutboxHeaderProjection {
	return DebeziumOutboxHeaderProjection{
		EventType:        string(evt.EventType),
		AggregateVersion: evt.AggregateVersion,
		TenantID:         evt.TenantID,
	}
}

func sampleActiveKnowledgeBase() *entity.KnowledgeBase {
	now := time.Now().UTC()
	return &entity.KnowledgeBase{
		TenantID:           "tenant-cdc-corp",
		KBID:               "kb-cdc-001",
		Name:               "CDC Knowledge Base",
		Description:        "Validating Debezium Outbox CDC event envelope",
		Version:            1,
		State:              entity.StateActive,
		RoutingPolicy:      entity.RoutingGreenOnly,
		ActiveIndexPointer: "idx_green_v1",
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
		TaxonomyTags: []string{"env:production", "tier:gold"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// TestOutboxCDC_AllSixDomainEvents verifies JSON payload serialization, completeness,
// and Debezium SMT contract compliance across all 6 domain event types (ADR-005).
func TestOutboxCDC_AllSixDomainEvents(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name       string
		eventType  event.EventType
		setupKB    func() *entity.KnowledgeBase
		extra      map[string]any
		assertions func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection)
	}{
		{
			name:      "1. KBCreated",
			eventType: event.EventKBCreated,
			setupKB:   sampleActiveKnowledgeBase,
			extra:     nil,
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["state"] != string(entity.StateActive) {
					t.Fatalf("expected state ACTIVE, got %v", payload["state"])
				}
				if payload["routing_policy"] != string(entity.RoutingGreenOnly) {
					t.Fatalf("expected routing_policy GREEN_ONLY, got %v", payload["routing_policy"])
				}
				if payload["candidate_index_pointer"] != "" {
					t.Fatalf("expected empty candidate_index_pointer, got %v", payload["candidate_index_pointer"])
				}
				if payload["candidate_schema"] != nil {
					t.Fatalf("expected nil candidate_schema, got %v", payload["candidate_schema"])
				}
				if headers.EventType != "KBCreated" {
					t.Fatalf("expected header eventType KBCreated, got %s", headers.EventType)
				}
			},
		},
		{
			name:      "2. KBUpdated",
			eventType: event.EventKBUpdated,
			setupKB: func() *entity.KnowledgeBase {
				kb := sampleActiveKnowledgeBase()
				kb.Version = 2
				kb.Name = "CDC Knowledge Base Updated"
				kb.TaxonomyTags = []string{"env:production", "tier:platinum"}
				return kb
			},
			extra: nil,
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["version"] != float64(2) {
					t.Fatalf("expected version 2, got %v", payload["version"])
				}
				if payload["name"] != "CDC Knowledge Base Updated" {
					t.Fatalf("expected updated name, got %v", payload["name"])
				}
				if headers.AggregateVersion != 2 {
					t.Fatalf("expected aggregateVersion header 2, got %d", headers.AggregateVersion)
				}
			},
		},
		{
			name:      "3. ReindexInitiated",
			eventType: event.EventReindexInitiated,
			setupKB: func() *entity.KnowledgeBase {
				kb := sampleActiveKnowledgeBase()
				kb.Version = 2
				kb.State = entity.StateReindexing
				kb.RoutingPolicy = entity.RoutingDualWrite
				kb.CandidateIndexPointer = "idx_blue_v2"
				kb.CandidateSchema = &entity.VectorSchema{
					EmbeddingModel: "text-embedding-3-large",
					Dimensions:     3072,
					DistanceMetric: entity.MetricCosine,
					SchemaVersion:  "v2.0",
				}
				return kb
			},
			extra: map[string]any{
				"migration_id":         "mig-001-blue",
				"source_green_pointer": "idx_green_v1",
				"target_blue_pointer":  "idx_blue_v2",
				"temporal_workflow_id": "wf-reindex-mig-001",
			},
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["state"] != string(entity.StateReindexing) {
					t.Fatalf("expected state REINDEXING, got %v", payload["state"])
				}
				if payload["routing_policy"] != string(entity.RoutingDualWrite) {
					t.Fatalf("expected routing_policy DUAL_WRITE, got %v", payload["routing_policy"])
				}
				if payload["migration_id"] != "mig-001-blue" {
					t.Fatalf("expected migration_id mig-001-blue, got %v", payload["migration_id"])
				}
				if payload["source_green_pointer"] != "idx_green_v1" {
					t.Fatalf("expected source_green_pointer idx_green_v1, got %v", payload["source_green_pointer"])
				}
				if payload["target_blue_pointer"] != "idx_blue_v2" {
					t.Fatalf("expected target_blue_pointer idx_blue_v2, got %v", payload["target_blue_pointer"])
				}
				if payload["temporal_workflow_id"] != "wf-reindex-mig-001" {
					t.Fatalf("expected temporal_workflow_id wf-reindex-mig-001, got %v", payload["temporal_workflow_id"])
				}
			},
		},
		{
			name:      "4. PointerPromoted",
			eventType: event.EventPointerPromoted,
			setupKB: func() *entity.KnowledgeBase {
				kb := sampleActiveKnowledgeBase()
				kb.Version = 3
				kb.State = entity.StateActive
				kb.RoutingPolicy = entity.RoutingGreenOnly
				kb.ActiveIndexPointer = "idx_blue_v2"
				kb.CandidateIndexPointer = ""
				kb.CandidateSchema = nil
				kb.ActiveSchema = entity.VectorSchema{
					EmbeddingModel: "text-embedding-3-large",
					Dimensions:     3072,
					DistanceMetric: entity.MetricCosine,
					SchemaVersion:  "v2.0",
				}
				return kb
			},
			extra: map[string]any{
				"migration_id":          "mig-001-blue",
				"retired_green_pointer": "idx_green_v1",
				"promoted_blue_pointer": "idx_blue_v2",
				"verified_delta_lag":    uint64(0),
			},
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["state"] != string(entity.StateActive) {
					t.Fatalf("expected state ACTIVE, got %v", payload["state"])
				}
				if payload["active_index_pointer"] != "idx_blue_v2" {
					t.Fatalf("expected active pointer idx_blue_v2, got %v", payload["active_index_pointer"])
				}
				if payload["retired_green_pointer"] != "idx_green_v1" {
					t.Fatalf("expected retired pointer idx_green_v1, got %v", payload["retired_green_pointer"])
				}
				if payload["verified_delta_lag"] != float64(0) {
					t.Fatalf("expected verified_delta_lag 0, got %v", payload["verified_delta_lag"])
				}
			},
		},
		{
			name:      "5. ReindexAborted",
			eventType: event.EventReindexAborted,
			setupKB: func() *entity.KnowledgeBase {
				kb := sampleActiveKnowledgeBase()
				kb.Version = 3
				kb.State = entity.StateActive
				kb.RoutingPolicy = entity.RoutingGreenOnly
				kb.ActiveIndexPointer = "idx_green_v1"
				kb.CandidateIndexPointer = ""
				kb.CandidateSchema = nil
				return kb
			},
			extra: map[string]any{
				"migration_id":          "mig-001-blue",
				"orphaned_blue_pointer": "idx_blue_v2",
				"abort_reason":          "delta lag buffer stalled",
			},
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["migration_id"] != "mig-001-blue" {
					t.Fatalf("expected migration_id mig-001-blue, got %v", payload["migration_id"])
				}
				if payload["orphaned_blue_pointer"] != "idx_blue_v2" {
					t.Fatalf("expected orphaned_blue_pointer idx_blue_v2, got %v", payload["orphaned_blue_pointer"])
				}
				if payload["abort_reason"] != "delta lag buffer stalled" {
					t.Fatalf("expected abort_reason 'delta lag buffer stalled', got %v", payload["abort_reason"])
				}
			},
		},
		{
			name:      "6. KBDeleted",
			eventType: event.EventKBDeleted,
			setupKB: func() *entity.KnowledgeBase {
				kb := sampleActiveKnowledgeBase()
				kb.Version = 2
				kb.State = entity.StateTombstoned
				return kb
			},
			extra: map[string]any{
				"candidate_index_pointer":     "idx_blue_v2",
				"active_migrations_cancelled": 1,
			},
			assertions: func(t *testing.T, payload map[string]any, headers DebeziumOutboxHeaderProjection) {
				if payload["state"] != string(entity.StateTombstoned) {
					t.Fatalf("expected state TOMBSTONED, got %v", payload["state"])
				}
				if payload["candidate_index_pointer"] != "idx_blue_v2" {
					t.Fatalf("expected candidate_index_pointer idx_blue_v2, got %v", payload["candidate_index_pointer"])
				}
				if payload["active_migrations_cancelled"] != float64(1) {
					t.Fatalf("expected active_migrations_cancelled 1, got %v", payload["active_migrations_cancelled"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kb := tc.setupKB()
			outboxEvt, err := event.NewOutboxEvent(kb, tc.eventType, tc.extra, now)
			if err != nil {
				t.Fatalf("unexpected error creating OutboxEvent: %v", err)
			}

			// 1. Verify envelope metadata
			if !uuidv4Regex.MatchString(outboxEvt.EventID) {
				t.Fatalf("expected valid UUIDv4 event_id, got %q", outboxEvt.EventID)
			}
			if outboxEvt.TenantID != kb.TenantID {
				t.Fatalf("expected tenant_id %q, got %q", kb.TenantID, outboxEvt.TenantID)
			}
			if outboxEvt.AggregateType != "KnowledgeBase" {
				t.Fatalf("expected aggregate_type KnowledgeBase, got %q", outboxEvt.AggregateType)
			}
			if outboxEvt.AggregateID != kb.KBID {
				t.Fatalf("expected aggregate_id %q, got %q", kb.KBID, outboxEvt.AggregateID)
			}
			if outboxEvt.EventType != tc.eventType {
				t.Fatalf("expected event_type %q, got %q", tc.eventType, outboxEvt.EventType)
			}
			if outboxEvt.AggregateVersion != kb.Version {
				t.Fatalf("expected aggregate_version %d, got %d", kb.Version, outboxEvt.AggregateVersion)
			}

			// 2. Verify payload serialization & deserialization
			var payload map[string]any
			if err := json.Unmarshal(outboxEvt.Payload, &payload); err != nil {
				t.Fatalf("failed to unmarshal JSON payload: %v", err)
			}

			if payload["event_id"] != outboxEvt.EventID {
				t.Fatalf("payload event_id mismatch: expected %q, got %v", outboxEvt.EventID, payload["event_id"])
			}
			if payload["tenant_id"] != kb.TenantID {
				t.Fatalf("payload tenant_id mismatch: expected %q, got %v", kb.TenantID, payload["tenant_id"])
			}
			if payload["kb_id"] != kb.KBID {
				t.Fatalf("payload kb_id mismatch: expected %q, got %v", kb.KBID, payload["kb_id"])
			}
			if payload["occurred_at"] == "" {
				t.Fatalf("payload occurred_at must not be empty")
			}

			// 3. Verify Debezium SMT Kafka header projection
			headers := simulateDebeziumHeaderExtraction(outboxEvt)
			if headers.TenantID != kb.TenantID {
				t.Fatalf("header tenantId mismatch: expected %q, got %q", kb.TenantID, headers.TenantID)
			}
			if headers.AggregateVersion != kb.Version {
				t.Fatalf("header aggregateVersion mismatch: expected %d, got %d", kb.Version, headers.AggregateVersion)
			}
			if headers.EventType != string(tc.eventType) {
				t.Fatalf("header eventType mismatch: expected %q, got %q", tc.eventType, headers.EventType)
			}

			// 4. Run event-specific assertions
			tc.assertions(t, payload, headers)
		})
	}
}
