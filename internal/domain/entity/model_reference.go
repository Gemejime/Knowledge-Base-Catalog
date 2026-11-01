package entity

import (
	"fmt"
	"strings"
)

// ModelConstraint defines the allowed dimensions and distance metrics for a supported embedding model (ADR-006).
type ModelConstraint struct {
	ModelName      string
	AllowedDims    []uint32
	AllowedMetrics []DistanceMetric
}

// ModelRegistry validates vector schemas against the static declarative catalog of supported embedding models.
type ModelRegistry struct {
	models map[string]ModelConstraint
}

var defaultRegistry = &ModelRegistry{
	models: map[string]ModelConstraint{
		"text-embedding-3-small": {
			ModelName:      "text-embedding-3-small",
			AllowedDims:    []uint32{512, 768, 1536},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct, MetricL2},
		},
		"text-embedding-3-large": {
			ModelName:      "text-embedding-3-large",
			AllowedDims:    []uint32{256, 768, 1024, 1536, 3072},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct, MetricL2},
		},
		"bge-large-en-v1.5": {
			ModelName:      "bge-large-en-v1.5",
			AllowedDims:    []uint32{1024},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct, MetricL2},
		},
		"bge-m3": {
			ModelName:      "bge-m3",
			AllowedDims:    []uint32{1024},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct, MetricL2},
		},
		"nv-embed-v2": {
			ModelName:      "nv-embed-v2",
			AllowedDims:    []uint32{4096},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct},
		},
		"e5-mistral-7b-instruct": {
			ModelName:      "e5-mistral-7b-instruct",
			AllowedDims:    []uint32{4096},
			AllowedMetrics: []DistanceMetric{MetricCosine, MetricDotProduct},
		},
	},
}

// DefaultModelRegistry returns the singleton Static Validated Model Registry (ADR-006).
func DefaultModelRegistry() *ModelRegistry {
	return defaultRegistry
}

// ValidateSchema verifies that the VectorSchema specifies a registered embedding model,
// a valid dimension size for that model, an allowed distance metric, and a non-empty schema_version.
func (r *ModelRegistry) ValidateSchema(schema VectorSchema) error {
	modelName := strings.TrimSpace(schema.EmbeddingModel)
	if modelName == "" {
		return fmt.Errorf("%w: embedding_model is required", ErrInvalidInput)
	}
	if strings.TrimSpace(schema.SchemaVersion) == "" {
		return fmt.Errorf("%w: schema_version is required", ErrInvalidInput)
	}
	if schema.Dimensions == 0 {
		return fmt.Errorf("%w: dimensions must be > 0", ErrInvalidInput)
	}
	if !schema.DistanceMetric.IsValid() {
		return fmt.Errorf("%w: unsupported distance_metric %q", ErrInvalidInput, schema.DistanceMetric)
	}

	constraint, exists := r.models[modelName]
	if !exists {
		return fmt.Errorf("%w: unsupported embedding_model %q in static model registry", ErrInvalidInput, modelName)
	}

	dimAllowed := false
	for _, d := range constraint.AllowedDims {
		if d == schema.Dimensions {
			dimAllowed = true
			break
		}
	}
	if !dimAllowed {
		return fmt.Errorf("%w: dimensions %d not supported for model %q", ErrInvalidInput, schema.Dimensions, modelName)
	}

	metricAllowed := false
	for _, m := range constraint.AllowedMetrics {
		if m == schema.DistanceMetric {
			metricAllowed = true
			break
		}
	}
	if !metricAllowed {
		return fmt.Errorf("%w: distance_metric %q not supported for model %q", ErrInvalidInput, schema.DistanceMetric, modelName)
	}

	return nil
}
