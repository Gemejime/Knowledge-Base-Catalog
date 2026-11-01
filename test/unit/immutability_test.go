package unit_test

import (
	"errors"
	"testing"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
)

func TestValidateVectorSchemaImmutability(t *testing.T) {
	baseSchema := entity.VectorSchema{
		EmbeddingModel: "text-embedding-3-large",
		Dimensions:     1536,
		DistanceMetric: entity.MetricCosine,
		SchemaVersion:  "v1",
	}

	t.Run("nil mutation is allowed", func(t *testing.T) {
		if err := policy.ValidateVectorSchemaImmutability(baseSchema, nil); err != nil {
			t.Fatalf("expected nil error for nil schema mutation, got %v", err)
		}
	})

	t.Run("identical schema is allowed", func(t *testing.T) {
		same := baseSchema
		if err := policy.ValidateVectorSchemaImmutability(baseSchema, &same); err != nil {
			t.Fatalf("expected nil error for identical schema, got %v", err)
		}
	})

	t.Run("mutating dimensions returns ErrStructuralMutationDisallowed", func(t *testing.T) {
		mut := baseSchema
		mut.Dimensions = 768
		err := policy.ValidateVectorSchemaImmutability(baseSchema, &mut)
		if !errors.Is(err, entity.ErrStructuralMutationDisallowed) {
			t.Fatalf("expected ErrStructuralMutationDisallowed on dimension mutation (1536 -> 768), got %v", err)
		}
	})

	t.Run("mutating distance metric returns ErrStructuralMutationDisallowed", func(t *testing.T) {
		mut := baseSchema
		mut.DistanceMetric = entity.MetricL2
		err := policy.ValidateVectorSchemaImmutability(baseSchema, &mut)
		if !errors.Is(err, entity.ErrStructuralMutationDisallowed) {
			t.Fatalf("expected ErrStructuralMutationDisallowed on distance_metric mutation (COSINE -> L2), got %v", err)
		}
	})

	t.Run("mutating embedding model returns ErrStructuralMutationDisallowed", func(t *testing.T) {
		mut := baseSchema
		mut.EmbeddingModel = "bge-m3"
		err := policy.ValidateVectorSchemaImmutability(baseSchema, &mut)
		if !errors.Is(err, entity.ErrStructuralMutationDisallowed) {
			t.Fatalf("expected ErrStructuralMutationDisallowed on embedding_model mutation, got %v", err)
		}
	})

	t.Run("mutating schema version returns ErrStructuralMutationDisallowed", func(t *testing.T) {
		mut := baseSchema
		mut.SchemaVersion = "v2"
		err := policy.ValidateVectorSchemaImmutability(baseSchema, &mut)
		if !errors.Is(err, entity.ErrStructuralMutationDisallowed) {
			t.Fatalf("expected ErrStructuralMutationDisallowed on schema_version mutation, got %v", err)
		}
	})
}

func TestStaticValidatedModelRegistry(t *testing.T) {
	reg := entity.DefaultModelRegistry()

	validCases := []entity.VectorSchema{
		{EmbeddingModel: "text-embedding-3-large", Dimensions: 1536, DistanceMetric: entity.MetricCosine, SchemaVersion: "v1"},
		{EmbeddingModel: "text-embedding-3-small", Dimensions: 768, DistanceMetric: entity.MetricDotProduct, SchemaVersion: "v1"},
		{EmbeddingModel: "bge-m3", Dimensions: 1024, DistanceMetric: entity.MetricL2, SchemaVersion: "v1"},
		{EmbeddingModel: "nv-embed-v2", Dimensions: 4096, DistanceMetric: entity.MetricCosine, SchemaVersion: "v1"},
	}
	for _, tc := range validCases {
		if err := reg.ValidateSchema(tc); err != nil {
			t.Fatalf("expected valid schema %+v, got error: %v", tc, err)
		}
	}

	invalidCases := []struct {
		name   string
		schema entity.VectorSchema
	}{
		{
			name:   "unknown model",
			schema: entity.VectorSchema{EmbeddingModel: "unknown-model-x", Dimensions: 1536, DistanceMetric: entity.MetricCosine, SchemaVersion: "v1"},
		},
		{
			name:   "unsupported dimension for bge-m3",
			schema: entity.VectorSchema{EmbeddingModel: "bge-m3", Dimensions: 1536, DistanceMetric: entity.MetricCosine, SchemaVersion: "v1"},
		},
		{
			name:   "unsupported L2 metric for nv-embed-v2",
			schema: entity.VectorSchema{EmbeddingModel: "nv-embed-v2", Dimensions: 4096, DistanceMetric: entity.MetricL2, SchemaVersion: "v1"},
		},
		{
			name:   "empty schema_version",
			schema: entity.VectorSchema{EmbeddingModel: "bge-m3", Dimensions: 1024, DistanceMetric: entity.MetricCosine, SchemaVersion: ""},
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			err := reg.ValidateSchema(tc.schema)
			if !errors.Is(err, entity.ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput for %s, got %v", tc.name, err)
			}
		})
	}
}
