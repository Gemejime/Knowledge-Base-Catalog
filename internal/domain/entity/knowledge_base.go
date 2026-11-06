package entity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Domain sentinel errors mapped deterministically to HTTP and gRPC status codes.
var (
	ErrKnowledgeBaseNotFound        = errors.New("knowledge base not found within tenant scope")
	ErrDuplicateKBName              = errors.New("knowledge base name already exists for tenant")
	ErrVersionConflict              = errors.New("optimistic concurrency version conflict")
	ErrStructuralMutationDisallowed = errors.New("in-place structural vector schema mutation is disallowed; trigger Blue-Green re-indexing")
	ErrInvalidStateTransition       = errors.New("invalid knowledge base lifecycle state transition")
	ErrDeltaLagNotZero              = errors.New("cannot promote index pointer while authoritative delta replay buffer lag > 0")
	ErrInsufficientPermissions      = errors.New("insufficient RBAC permissions for requested operation")
	ErrTenantMismatch               = errors.New("cross-tenant operation rejected: request tenant_id does not match verified context")
	ErrIdempotencyKeyConflict       = errors.New("idempotency key already used with a different request payload")
	ErrInvalidInput                 = errors.New("invalid input parameter")
)

// LifecycleState represents the administrative state of a Knowledge Base.
type LifecycleState string

const (
	StateCreating   LifecycleState = "CREATING"
	StateActive     LifecycleState = "ACTIVE"
	StateReindexing LifecycleState = "REINDEXING"
	StatePromoted   LifecycleState = "PROMOTED"
	StateAborted    LifecycleState = "ABORTED"
	StateTombstoned LifecycleState = "TOMBSTONED"
)

// IsValid checks whether the lifecycle state is a recognized enum value.
func (s LifecycleState) IsValid() bool {
	switch s {
	case StateCreating, StateActive, StateReindexing, StatePromoted, StateAborted, StateTombstoned:
		return true
	default:
		return false
	}
}

// RoutingPolicy governs ingestion write routing during normal operation vs. Blue-Green migration.
type RoutingPolicy string

const (
	RoutingGreenOnly RoutingPolicy = "GREEN_ONLY"
	RoutingDualWrite RoutingPolicy = "DUAL_WRITE"
)

// DistanceMetric defines the vector similarity metric for HNSW indices.
type DistanceMetric string

const (
	MetricCosine     DistanceMetric = "COSINE"
	MetricDotProduct DistanceMetric = "DOT_PRODUCT"
	MetricL2         DistanceMetric = "L2"
)

// IsValid checks if the distance metric is supported.
func (m DistanceMetric) IsValid() bool {
	switch m {
	case MetricCosine, MetricDotProduct, MetricL2:
		return true
	default:
		return false
	}
}

// VectorSchema encapsulates immutable physical vector index parameters (ADR-001).
type VectorSchema struct {
	EmbeddingModel string         `json:"embedding_model"`
	Dimensions     uint32         `json:"dimensions"`
	DistanceMetric DistanceMetric `json:"distance_metric"`
	SchemaVersion  string         `json:"schema_version"`
}

// Equals returns true if two VectorSchema definitions are structurally identical.
func (s VectorSchema) Equals(other VectorSchema) bool {
	return s.EmbeddingModel == other.EmbeddingModel &&
		s.Dimensions == other.Dimensions &&
		s.DistanceMetric == other.DistanceMetric &&
		s.SchemaVersion == other.SchemaVersion
}

// RetrievalProfile holds mutable search hyperparameters.
type RetrievalProfile struct {
	DefaultTopK                     uint32  `json:"default_top_k"`
	DenseWeight                     float32 `json:"dense_weight"`
	SparseWeight                    float32 `json:"sparse_weight"`
	RerankerCandidatePoolSize       uint32  `json:"reranker_candidate_pool_size"`
	CrossEncoderConfidenceThreshold float32 `json:"cross_encoder_confidence_threshold"`
}

// Validate enforces boundary constraints on retrieval hyperparameters.
func (p RetrievalProfile) Validate() error {
	if p.DefaultTopK < 1 || p.DefaultTopK > 200 {
		return fmt.Errorf("%w: default_top_k must be between 1 and 200", ErrInvalidInput)
	}
	if math.IsNaN(float64(p.DenseWeight)) || p.DenseWeight < 0 || p.DenseWeight > 1 {
		return fmt.Errorf("%w: dense_weight must be between 0.0 and 1.0", ErrInvalidInput)
	}
	if math.IsNaN(float64(p.SparseWeight)) || p.SparseWeight < 0 || p.SparseWeight > 1 {
		return fmt.Errorf("%w: sparse_weight must be between 0.0 and 1.0", ErrInvalidInput)
	}
	if p.DenseWeight == 0 && p.SparseWeight == 0 {
		return fmt.Errorf("%w: dense_weight and sparse_weight cannot both be 0", ErrInvalidInput)
	}
	if p.RerankerCandidatePoolSize < 1 {
		return fmt.Errorf("%w: reranker_candidate_pool_size must be >= 1", ErrInvalidInput)
	}
	if math.IsNaN(float64(p.CrossEncoderConfidenceThreshold)) || p.CrossEncoderConfidenceThreshold < 0 || p.CrossEncoderConfidenceThreshold > 1 {
		return fmt.Errorf("%w: cross_encoder_confidence_threshold must be between 0.0 and 1.0", ErrInvalidInput)
	}
	return nil
}

// KnowledgeBase is the aggregate root for a tenant's Knowledge Base metadata and pointer state.
type KnowledgeBase struct {
	TenantID              string           `json:"tenant_id"`
	KBID                  string           `json:"kb_id"`
	Name                  string           `json:"name"`
	Description           string           `json:"description"`
	Version               uint64           `json:"version,string"`
	State                 LifecycleState   `json:"state"`
	RoutingPolicy         RoutingPolicy    `json:"routing_policy"`
	ActiveIndexPointer    string           `json:"active_index_pointer"`
	CandidateIndexPointer string           `json:"candidate_index_pointer,omitempty"`
	ActiveSchema          VectorSchema     `json:"active_schema"`
	CandidateSchema       *VectorSchema    `json:"candidate_schema,omitempty"`
	RetrievalProfile      RetrievalProfile `json:"retrieval_profile"`
	TaxonomyTags          []string         `json:"taxonomy_tags"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

// NewUUIDv4 generates a standard RFC 4122 UUIDv4 string using crypto/rand without external dependencies.
func NewUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to read random bytes for uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant RFC 4122
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:]), nil
}

// NewKnowledgeBase creates a validated KnowledgeBase aggregate, transitioning from CREATING to ACTIVE.
func NewKnowledgeBase(
	tenantID string,
	kbID string,
	name string,
	description string,
	schema VectorSchema,
	profile RetrievalProfile,
	rawTags []string,
	now time.Time,
) (*KnowledgeBase, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", ErrInvalidInput)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if kbID == "" {
		var err error
		kbID, err = NewUUIDv4()
		if err != nil {
			return nil, err
		}
	}
	if err := DefaultModelRegistry().ValidateSchema(schema); err != nil {
		return nil, err
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	normalizedTags, err := NormalizeTaxonomyTags(rawTags)
	if err != nil {
		return nil, err
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	kb := &KnowledgeBase{
		TenantID:              tenantID,
		KBID:                  kbID,
		Name:                  name,
		Description:           strings.TrimSpace(description),
		Version:               1,
		State:                 StateCreating,
		RoutingPolicy:         RoutingGreenOnly,
		ActiveIndexPointer:    FormatGreenPointer(tenantID, kbID, 1),
		CandidateIndexPointer: "",
		ActiveSchema:          schema,
		CandidateSchema:       nil,
		RetrievalProfile:      profile,
		TaxonomyTags:          normalizedTags,
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	// Transition CREATING -> ACTIVE during initial creation handshake.
	if err := ValidateTransition(kb.State, StateActive, 0); err != nil {
		return nil, err
	}
	kb.State = StateActive
	return kb, nil
}

// FormatGreenPointer returns a deterministic Green physical index pointer name.
func FormatGreenPointer(tenantID, kbID string, version uint64) string {
	return fmt.Sprintf("idx_%s_%s_green_v%d", sanitizePointerToken(tenantID), sanitizePointerToken(kbID), version)
}

// FormatBluePointer returns a deterministic Blue candidate index pointer name.
func FormatBluePointer(tenantID, kbID string, version uint64) string {
	return fmt.Sprintf("idx_%s_%s_blue_v%d", sanitizePointerToken(tenantID), sanitizePointerToken(kbID), version)
}

func sanitizePointerToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
