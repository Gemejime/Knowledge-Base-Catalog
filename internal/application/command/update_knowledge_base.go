package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

// UpdateKnowledgeBaseCommand coordinates mutable metadata updates guarded by vector schema immutability (ADR-001) and OCC (ADR-004).
type UpdateKnowledgeBaseCommand struct {
	repo  repository.CatalogRepository
	cache repository.VersionedCache
}

// NewUpdateKnowledgeBaseCommand constructs an UpdateKnowledgeBaseCommand.
func NewUpdateKnowledgeBaseCommand(repo repository.CatalogRepository, cache repository.VersionedCache) *UpdateKnowledgeBaseCommand {
	return &UpdateKnowledgeBaseCommand{repo: repo, cache: cache}
}

// Execute validates immutability constraints, applies partial field updates, and commits version + 1 with KBUpdated outbox event.
func (c *UpdateKnowledgeBaseCommand) Execute(ctx context.Context, in dto.UpdateKBInput) (*dto.KBResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionUpdateKB); err != nil {
		return nil, err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return nil, fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}
	if in.ExpectedVersion == 0 {
		return nil, fmt.Errorf("%w: expected_version must be >= 1", entity.ErrInvalidInput)
	}

	kb, err := c.repo.GetByID(ctx, in.Caller.TenantID, kbID)
	if err != nil {
		return nil, err
	}

	// Enforce ADR-001: Reject any in-place vector schema mutation before checking or bumping version.
	if err := policy.ValidateVectorSchemaImmutability(kb.ActiveSchema, in.AttemptedSchemaMutation); err != nil {
		return nil, err
	}

	if kb.Version != in.ExpectedVersion {
		return nil, fmt.Errorf("%w: expected version %d, actual %d", entity.ErrVersionConflict, in.ExpectedVersion, kb.Version)
	}

	maskSet := make(map[string]bool, len(in.UpdateMask))
	for _, field := range in.UpdateMask {
		f := strings.TrimSpace(field)
		if f == "active_schema" || f == "vector_schema" || f == "embedding_model" || f == "dimensions" || f == "distance_metric" {
			return nil, fmt.Errorf("%w: field %q cannot be mutated in-place via update_mask", entity.ErrStructuralMutationDisallowed, f)
		}
		if f != "" {
			maskSet[f] = true
		}
	}
	hasMask := len(maskSet) > 0

	if (!hasMask && in.Name != nil) || maskSet["name"] {
		if in.Name != nil {
			trimmed := strings.TrimSpace(*in.Name)
			if trimmed == "" {
				return nil, fmt.Errorf("%w: name cannot be empty", entity.ErrInvalidInput)
			}
			kb.Name = trimmed
		}
	}

	if (!hasMask && in.Description != nil) || maskSet["description"] {
		if in.Description != nil {
			kb.Description = strings.TrimSpace(*in.Description)
		} else if maskSet["description"] {
			kb.Description = ""
		}
	}

	if (!hasMask && in.RetrievalProfile != nil) || maskSet["retrieval_profile"] {
		if in.RetrievalProfile == nil {
			return nil, fmt.Errorf("%w: retrieval_profile is required when specified in update_mask", entity.ErrInvalidInput)
		}
		if err := in.RetrievalProfile.Validate(); err != nil {
			return nil, err
		}
		kb.RetrievalProfile = *in.RetrievalProfile
	}

	// Selected-field semantics for taxonomy_tags:
	// - If update_mask includes "taxonomy_tags" (or update_mask is omitted and TaxonomyTagsPresent is true),
	//   apply the normalized tags (allowing [] to clear all tags).
	// - Otherwise, preserve existing kb.TaxonomyTags.
	if maskSet["taxonomy_tags"] || (!hasMask && in.TaxonomyTagsPresent) {
		normalizedTags, err := entity.NormalizeTaxonomyTags(in.TaxonomyTags)
		if err != nil {
			return nil, err
		}
		kb.TaxonomyTags = normalizedTags
	}

	now := time.Now().UTC()
	kb.Version = in.ExpectedVersion + 1
	kb.UpdatedAt = now

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventKBUpdated, nil, now)
	if err != nil {
		return nil, err
	}

	if err := c.repo.UpdateMetadata(ctx, kb, in.ExpectedVersion, outboxEvt); err != nil {
		return nil, err
	}

	if c.cache != nil {
		_ = c.cache.PutVersion(ctx, kb)
	}

	return &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}, nil
}
