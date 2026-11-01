package dto

import (
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
)

// CreateKBInput represents the application input for creating a Knowledge Base.
type CreateKBInput struct {
	Caller           policy.CallerIdentity
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	VectorSchema     entity.VectorSchema     `json:"vector_schema"`
	RetrievalProfile entity.RetrievalProfile `json:"retrieval_profile"`
	TaxonomyTags     []string                `json:"taxonomy_tags"`
	IdempotencyKey   string                  `json:"idempotency_key,omitempty"`
	HeaderIdempKey   string                  `json:"-"`
}

// UpdateKBInput represents a partial metadata update guarded by Optimistic Concurrency Control.
type UpdateKBInput struct {
	Caller                  policy.CallerIdentity
	KBID                    string                   `json:"-"`
	ExpectedVersion         uint64                   `json:"expected_version"`
	Name                    *string                  `json:"name,omitempty"`
	Description             *string                  `json:"description,omitempty"`
	RetrievalProfile        *entity.RetrievalProfile `json:"retrieval_profile,omitempty"`
	TaxonomyTags            []string                 `json:"taxonomy_tags,omitempty"`
	TaxonomyTagsPresent     bool                     `json:"-"`
	UpdateMask              []string                 `json:"update_mask,omitempty"`
	AttemptedSchemaMutation *entity.VectorSchema     `json:"attempted_schema_mutation,omitempty"`
}

// TriggerReindexingInput initiates a Blue-Green schema migration.
type TriggerReindexingInput struct {
	Caller           policy.CallerIdentity
	KBID             string              `json:"-"`
	ExpectedVersion  uint64              `json:"expected_version"`
	TargetBlueSchema entity.VectorSchema `json:"target_blue_schema"`
}

// PromotePointerInput promotes the Blue candidate pointer to active when authoritative delta lag == 0.
type PromotePointerInput struct {
	Caller                  policy.CallerIdentity
	KBID                    string  `json:"-"`
	ExpectedVersion         uint64  `json:"expected_version"`
	VerifiedDeltaLagRecords *uint64 `json:"verified_delta_lag_records,omitempty"`
}

// AbortReindexingInput aborts an active Blue-Green migration and retains the Green pointer.
type AbortReindexingInput struct {
	Caller          policy.CallerIdentity
	KBID            string `json:"-"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason"`
}

// DeleteKBInput soft-deletes (tombstones) a Knowledge Base under OCC.
type DeleteKBInput struct {
	Caller          policy.CallerIdentity
	KBID            string
	ExpectedVersion uint64
}

// GetKBInput retrieves a Knowledge Base by ID with optional causal freshness min_version.
type GetKBInput struct {
	Caller     policy.CallerIdentity
	KBID       string
	MinVersion uint64
}

// ListKBsInput lists Knowledge Bases within the caller's tenant scope filtered by taxonomy tags.
type ListKBsInput struct {
	Caller          policy.CallerIdentity
	TaxonomyFilters []string
	PageSize        int
	PageToken       string
}

// KBResponse wraps a KnowledgeBase domain aggregate for transport serialization.
type KBResponse struct {
	KnowledgeBase *entity.KnowledgeBase `json:"knowledge_base"`
}

// ListKBsResponse wraps a paginated slice of KnowledgeBases.
type ListKBsResponse struct {
	KnowledgeBases []*entity.KnowledgeBase `json:"knowledge_bases"`
	NextPageToken  string                  `json:"next_page_token,omitempty"`
}

// MigrationResponse represents the response emitted upon initiating Blue-Green re-indexing.
type MigrationResponse struct {
	TenantID              string                `json:"tenant_id"`
	KBID                  string                `json:"kb_id"`
	MigrationID           string                `json:"migration_id"`
	State                 entity.LifecycleState `json:"state"`
	RoutingPolicy         entity.RoutingPolicy  `json:"routing_policy"`
	CandidateIndexPointer string                `json:"candidate_index_pointer"`
	Version               uint64                `json:"version"`
}

// CloneKB returns a deep copy of a KnowledgeBase to prevent shared slice/pointer mutation across goroutines.
func CloneKB(src *entity.KnowledgeBase) *entity.KnowledgeBase {
	if src == nil {
		return nil
	}
	cp := *src
	if src.CandidateSchema != nil {
		cs := *src.CandidateSchema
		cp.CandidateSchema = &cs
	}
	if src.TaxonomyTags != nil {
		tags := make([]string, len(src.TaxonomyTags))
		copy(tags, src.TaxonomyTags)
		cp.TaxonomyTags = tags
	} else {
		cp.TaxonomyTags = []string{}
	}
	cp.CreatedAt = src.CreatedAt.UTC().Truncate(time.Microsecond)
	cp.UpdatedAt = src.UpdatedAt.UTC().Truncate(time.Microsecond)
	return &cp
}
