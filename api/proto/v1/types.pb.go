// Code generated from api/proto/v1/types.proto; DO NOT EDIT MANUALLY WITHOUT SYNCING types.proto.

package catalogv1

import (
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LifecycleState enumerates the administrative lifecycle states of a Knowledge Base.
type LifecycleState int32

const (
	LifecycleState_LIFECYCLE_STATE_UNSPECIFIED LifecycleState = 0
	LifecycleState_LIFECYCLE_STATE_CREATING    LifecycleState = 1
	LifecycleState_LIFECYCLE_STATE_ACTIVE      LifecycleState = 2
	LifecycleState_LIFECYCLE_STATE_REINDEXING  LifecycleState = 3
	LifecycleState_LIFECYCLE_STATE_PROMOTED    LifecycleState = 4
	LifecycleState_LIFECYCLE_STATE_ABORTED     LifecycleState = 5
	LifecycleState_LIFECYCLE_STATE_TOMBSTONED  LifecycleState = 6
)

// RoutingPolicy governs live ingestion routing during normal vs. Blue-Green migration states.
type RoutingPolicy int32

const (
	RoutingPolicy_ROUTING_POLICY_UNSPECIFIED RoutingPolicy = 0
	RoutingPolicy_ROUTING_POLICY_GREEN_ONLY  RoutingPolicy = 1
	RoutingPolicy_ROUTING_POLICY_DUAL_WRITE  RoutingPolicy = 2
)

// DistanceMetric defines the similarity metric for physical vector indices.
type DistanceMetric int32

const (
	DistanceMetric_DISTANCE_METRIC_UNSPECIFIED DistanceMetric = 0
	DistanceMetric_DISTANCE_METRIC_COSINE      DistanceMetric = 1
	DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT DistanceMetric = 2
	DistanceMetric_DISTANCE_METRIC_L2          DistanceMetric = 3
)

// VectorSchema defines the immutable vector index configuration (ADR-001).
type VectorSchema struct {
	EmbeddingModel string         `json:"embedding_model,omitempty"`
	Dimensions     uint32         `json:"dimensions,omitempty"`
	DistanceMetric DistanceMetric `json:"distance_metric,omitempty"`
	SchemaVersion  string         `json:"schema_version,omitempty"`
}

func (x *VectorSchema) GetEmbeddingModel() string {
	if x != nil {
		return x.EmbeddingModel
	}
	return ""
}

func (x *VectorSchema) GetDimensions() uint32 {
	if x != nil {
		return x.Dimensions
	}
	return 0
}

func (x *VectorSchema) GetDistanceMetric() DistanceMetric {
	if x != nil {
		return x.DistanceMetric
	}
	return DistanceMetric_DISTANCE_METRIC_UNSPECIFIED
}

func (x *VectorSchema) GetSchemaVersion() string {
	if x != nil {
		return x.SchemaVersion
	}
	return ""
}

// RetrievalProfile defines mutable search hyperparameters.
type RetrievalProfile struct {
	DefaultTopK                     uint32  `json:"default_top_k,omitempty"`
	DenseWeight                     float32 `json:"dense_weight,omitempty"`
	SparseWeight                    float32 `json:"sparse_weight,omitempty"`
	RerankerCandidatePoolSize       uint32  `json:"reranker_candidate_pool_size,omitempty"`
	CrossEncoderConfidenceThreshold float32 `json:"cross_encoder_confidence_threshold,omitempty"`
}

// KnowledgeBase is the Protobuf representation of a tenant's Knowledge Base metadata and pointer state.
type KnowledgeBase struct {
	TenantId              string                 `json:"tenant_id,omitempty"`
	KbId                  string                 `json:"kb_id,omitempty"`
	Name                  string                 `json:"name,omitempty"`
	Description           string                 `json:"description,omitempty"`
	Version               uint64                 `json:"version,omitempty"`
	State                 LifecycleState         `json:"state,omitempty"`
	RoutingPolicy         RoutingPolicy          `json:"routing_policy,omitempty"`
	ActiveIndexPointer    string                 `json:"active_index_pointer,omitempty"`
	CandidateIndexPointer string                 `json:"candidate_index_pointer,omitempty"`
	ActiveSchema          *VectorSchema          `json:"active_schema,omitempty"`
	CandidateSchema       *VectorSchema          `json:"candidate_schema,omitempty"`
	RetrievalProfile      *RetrievalProfile      `json:"retrieval_profile,omitempty"`
	TaxonomyTags          []string               `json:"taxonomy_tags,omitempty"`
	CreatedAt             *timestamppb.Timestamp `json:"created_at,omitempty"`
	UpdatedAt             *timestamppb.Timestamp `json:"updated_at,omitempty"`
}

// CreateKBRequest represents the gRPC request for CreateKnowledgeBase.
type CreateKBRequest struct {
	TenantId         string            `json:"tenant_id,omitempty"`
	Name             string            `json:"name,omitempty"`
	Description      string            `json:"description,omitempty"`
	VectorSchema     *VectorSchema     `json:"vector_schema,omitempty"`
	RetrievalProfile *RetrievalProfile `json:"retrieval_profile,omitempty"`
	TaxonomyTags     []string          `json:"taxonomy_tags,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
}

func (x *CreateKBRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// UpdateKBRequest represents the gRPC request for UpdateKnowledgeBase.
type UpdateKBRequest struct {
	TenantId                string                 `json:"tenant_id,omitempty"`
	KbId                    string                 `json:"kb_id,omitempty"`
	ExpectedVersion         uint64                 `json:"expected_version,omitempty"`
	Name                    *string                `json:"name,omitempty"`
	Description             *string                `json:"description,omitempty"`
	RetrievalProfile        *RetrievalProfile      `json:"retrieval_profile,omitempty"`
	TaxonomyTags            []string               `json:"taxonomy_tags,omitempty"`
	AttemptedSchemaMutation *VectorSchema          `json:"attempted_schema_mutation,omitempty"`
	UpdateMask              *fieldmaskpb.FieldMask `json:"update_mask,omitempty"`
}

func (x *UpdateKBRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// GetKBRequest represents the gRPC request for GetKnowledgeBase.
type GetKBRequest struct {
	TenantId   string  `json:"tenant_id,omitempty"`
	KbId       string  `json:"kb_id,omitempty"`
	MinVersion *uint64 `json:"min_version,omitempty"`
}

func (x *GetKBRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// ListKBsRequest represents the gRPC request for ListKnowledgeBases.
type ListKBsRequest struct {
	TenantId        string   `json:"tenant_id,omitempty"`
	TaxonomyFilters []string `json:"taxonomy_filters,omitempty"`
	PageSize        uint32   `json:"page_size,omitempty"`
	PageToken       string   `json:"page_token,omitempty"`
}

func (x *ListKBsRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// ListKBsResponse represents the gRPC response for ListKnowledgeBases.
type ListKBsResponse struct {
	KnowledgeBases []*KnowledgeBase `json:"knowledge_bases,omitempty"`
	NextPageToken  string           `json:"next_page_token,omitempty"`
}

// DeleteKBRequest represents the gRPC request for DeleteKnowledgeBase.
type DeleteKBRequest struct {
	TenantId        string `json:"tenant_id,omitempty"`
	KbId            string `json:"kb_id,omitempty"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
}

func (x *DeleteKBRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// TriggerReindexingRequest represents the gRPC request for TriggerReindexing.
type TriggerReindexingRequest struct {
	TenantId         string        `json:"tenant_id,omitempty"`
	KbId             string        `json:"kb_id,omitempty"`
	ExpectedVersion  uint64        `json:"expected_version,omitempty"`
	TargetBlueSchema *VectorSchema `json:"target_blue_schema,omitempty"`
}

func (x *TriggerReindexingRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// MigrationResponse represents the gRPC response for TriggerReindexing.
type MigrationResponse struct {
	TenantId              string         `json:"tenant_id,omitempty"`
	KbId                  string         `json:"kb_id,omitempty"`
	MigrationId           string         `json:"migration_id,omitempty"`
	State                 LifecycleState `json:"state,omitempty"`
	RoutingPolicy         RoutingPolicy  `json:"routing_policy,omitempty"`
	CandidateIndexPointer string         `json:"candidate_index_pointer,omitempty"`
	Version               uint64         `json:"version,omitempty"`
}

// PromotePointerRequest represents the gRPC request for PromoteIndexPointer.
type PromotePointerRequest struct {
	TenantId                string  `json:"tenant_id,omitempty"`
	KbId                    string  `json:"kb_id,omitempty"`
	ExpectedVersion         uint64  `json:"expected_version,omitempty"`
	VerifiedDeltaLagRecords *uint64 `json:"verified_delta_lag_records,omitempty"`
}

func (x *PromotePointerRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// AbortReindexingRequest represents the gRPC request for AbortReindexing.
type AbortReindexingRequest struct {
	TenantId        string `json:"tenant_id,omitempty"`
	KbId            string `json:"kb_id,omitempty"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (x *AbortReindexingRequest) GetTenantId() string {
	if x != nil {
		return x.TenantId
	}
	return ""
}

// KBResponse wraps a KnowledgeBase message.
type KBResponse struct {
	KnowledgeBase *KnowledgeBase `json:"knowledge_base,omitempty"`
}
