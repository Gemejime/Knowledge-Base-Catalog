package event

import (
	"encoding/json"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// EventType enumerates transactional CDC outbox event types tailed by Debezium (ADR-005).
type EventType string

const (
	EventKBCreated        EventType = "KBCreated"
	EventKBUpdated        EventType = "KBUpdated"
	EventReindexInitiated EventType = "ReindexInitiated"
	EventPointerPromoted  EventType = "PointerPromoted"
	EventReindexAborted   EventType = "ReindexAborted"
	EventKBDeleted        EventType = "KBDeleted"
)

// OutboxEvent represents an atomic CDC record written to outbox_events in the same SQL transaction as state mutations.
type OutboxEvent struct {
	EventID          string          `json:"event_id"`
	TenantID         string          `json:"tenant_id"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	EventType        EventType       `json:"event_type"`
	AggregateVersion uint64          `json:"aggregate_version"`
	Payload          json.RawMessage `json:"payload"`
	CreatedAt        time.Time       `json:"created_at"`
}

// NewOutboxEvent constructs a deterministic OutboxEvent for the given KnowledgeBase and event type.
func NewOutboxEvent(kb *entity.KnowledgeBase, eventType EventType, extra map[string]any, now time.Time) (*OutboxEvent, error) {
	eventID, err := entity.NewUUIDv4()
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	envelope := map[string]any{
		"event_id":                eventID,
		"event_type":              string(eventType),
		"tenant_id":               kb.TenantID,
		"kb_id":                   kb.KBID,
		"name":                    kb.Name,
		"version":                 kb.Version,
		"state":                   string(kb.State),
		"routing_policy":          string(kb.RoutingPolicy),
		"active_index_pointer":    kb.ActiveIndexPointer,
		"candidate_index_pointer": kb.CandidateIndexPointer,
		"active_schema":           kb.ActiveSchema,
		"candidate_schema":        kb.CandidateSchema,
		"taxonomy_tags":           kb.TaxonomyTags,
		"occurred_at":             now.Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		envelope[k] = v
	}

	payloadBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	return &OutboxEvent{
		EventID:          eventID,
		TenantID:         kb.TenantID,
		AggregateType:    "KnowledgeBase",
		AggregateID:      kb.KBID,
		EventType:        eventType,
		AggregateVersion: kb.Version,
		Payload:          payloadBytes,
		CreatedAt:        now,
	}, nil
}
