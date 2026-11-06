package policy

import (
	"fmt"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// ValidateVectorSchemaImmutability enforces ADR-001: in-place mutations to embedding_model,
// dimensions, distance_metric, or schema_version via UpdateKnowledgeBase are strictly forbidden
// and return entity.ErrStructuralMutationDisallowed.
func ValidateVectorSchemaImmutability(activeSchema entity.VectorSchema, attemptedMutation *entity.VectorSchema) error {
	if attemptedMutation == nil {
		return nil
	}
	if activeSchema.EmbeddingModel != attemptedMutation.EmbeddingModel {
		return fmt.Errorf(
			"%w: cannot mutate embedding_model in-place from %q to %q",
			entity.ErrStructuralMutationDisallowed,
			activeSchema.EmbeddingModel,
			attemptedMutation.EmbeddingModel,
		)
	}
	if activeSchema.Dimensions != attemptedMutation.Dimensions {
		return fmt.Errorf(
			"%w: cannot mutate dimensions in-place from %d to %d",
			entity.ErrStructuralMutationDisallowed,
			activeSchema.Dimensions,
			attemptedMutation.Dimensions,
		)
	}
	if activeSchema.DistanceMetric != attemptedMutation.DistanceMetric {
		return fmt.Errorf(
			"%w: cannot mutate distance_metric in-place from %q to %q",
			entity.ErrStructuralMutationDisallowed,
			activeSchema.DistanceMetric,
			attemptedMutation.DistanceMetric,
		)
	}
	if attemptedMutation.SchemaVersion != "" && activeSchema.SchemaVersion != attemptedMutation.SchemaVersion {
		return fmt.Errorf(
			"%w: cannot mutate schema_version in-place from %q to %q",
			entity.ErrStructuralMutationDisallowed,
			activeSchema.SchemaVersion,
			attemptedMutation.SchemaVersion,
		)
	}
	return nil
}
