package dto

import (
	"bytes"
	"encoding/json"
	"strconv"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
)

// Uint64String unmarshals a JSON string (or number) into uint64 and marshals as a string.
type Uint64String uint64

// UnmarshalJSON decodes a string or integer JSON value into Uint64String.
func (u *Uint64String) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		*u = Uint64String(v)
		return nil
	}
	var v uint64
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	*u = Uint64String(v)
	return nil
}

// MarshalJSON encodes Uint64String as a JSON string.
func (u Uint64String) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatUint(uint64(u), 10))
}

// OptionalUint64String unmarshals a JSON string (or number) into *uint64 and marshals as a string.
type OptionalUint64String struct {
	Value *uint64
}

// UnmarshalJSON decodes a string or integer JSON value into OptionalUint64String.
func (o *OptionalUint64String) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		o.Value = nil
		return nil
	}
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		o.Value = &v
		return nil
	}
	var v uint64
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// MarshalJSON encodes OptionalUint64String as a JSON string or null.
func (o OptionalUint64String) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(strconv.FormatUint(*o.Value, 10))
}

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
	ExpectedVersion         uint64                   `json:"expected_version,string"`
	Name                    *string                  `json:"name,omitempty"`
	Description             *string                  `json:"description,omitempty"`
	RetrievalProfile        *entity.RetrievalProfile `json:"retrieval_profile,omitempty"`
	TaxonomyTags            []string                 `json:"taxonomy_tags,omitempty"`
	TaxonomyTagsPresent     bool                     `json:"-"`
	UpdateMask              []string                 `json:"update_mask,omitempty"`
	AttemptedSchemaMutation *entity.VectorSchema     `json:"attempted_schema_mutation,omitempty"`
}

// UnmarshalJSON accepts both JSON string and integer encodings for expected_version.
func (in *UpdateKBInput) UnmarshalJSON(data []byte) error {
	type Alias UpdateKBInput
	aux := &struct {
		ExpectedVersion Uint64String `json:"expected_version"`
		*Alias
	}{
		Alias: (*Alias)(in),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	in.ExpectedVersion = uint64(aux.ExpectedVersion)
	return nil
}

// TriggerReindexingInput initiates a Blue-Green schema migration.
type TriggerReindexingInput struct {
	Caller           policy.CallerIdentity
	KBID             string              `json:"-"`
	ExpectedVersion  uint64              `json:"expected_version,string"`
	TargetBlueSchema entity.VectorSchema `json:"target_blue_schema"`
}

// UnmarshalJSON accepts both JSON string and integer encodings for expected_version.
func (in *TriggerReindexingInput) UnmarshalJSON(data []byte) error {
	type Alias TriggerReindexingInput
	aux := &struct {
		ExpectedVersion Uint64String `json:"expected_version"`
		*Alias
	}{
		Alias: (*Alias)(in),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	in.ExpectedVersion = uint64(aux.ExpectedVersion)
	return nil
}

// PromotePointerInput promotes the Blue candidate pointer to active when authoritative delta lag == 0.
type PromotePointerInput struct {
	Caller                  policy.CallerIdentity
	KBID                    string  `json:"-"`
	ExpectedVersion         uint64  `json:"expected_version,string"`
	VerifiedDeltaLagRecords *uint64 `json:"verified_delta_lag_records,omitempty,string"`
}

// UnmarshalJSON accepts both JSON string and integer encodings for expected_version and verified_delta_lag_records.
func (in *PromotePointerInput) UnmarshalJSON(data []byte) error {
	type Alias PromotePointerInput
	aux := &struct {
		ExpectedVersion         Uint64String         `json:"expected_version"`
		VerifiedDeltaLagRecords OptionalUint64String `json:"verified_delta_lag_records"`
		*Alias
	}{
		Alias: (*Alias)(in),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	in.ExpectedVersion = uint64(aux.ExpectedVersion)
	in.VerifiedDeltaLagRecords = aux.VerifiedDeltaLagRecords.Value
	return nil
}

// AbortReindexingInput aborts an active Blue-Green migration and retains the Green pointer.
type AbortReindexingInput struct {
	Caller          policy.CallerIdentity
	KBID            string `json:"-"`
	ExpectedVersion uint64 `json:"expected_version,string"`
	Reason          string `json:"reason"`
}

// UnmarshalJSON accepts both JSON string and integer encodings for expected_version.
func (in *AbortReindexingInput) UnmarshalJSON(data []byte) error {
	type Alias AbortReindexingInput
	aux := &struct {
		ExpectedVersion Uint64String `json:"expected_version"`
		*Alias
	}{
		Alias: (*Alias)(in),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	in.ExpectedVersion = uint64(aux.ExpectedVersion)
	return nil
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
	Version               uint64                `json:"version,string"`
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
