package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

// GetKnowledgeBaseQuery resolves Knowledge Base metadata via optional Redis Cluster versioned read with PostgreSQL fallback.
type GetKnowledgeBaseQuery struct {
	repo  repository.CatalogRepository
	cache repository.VersionedCache
}

// NewGetKnowledgeBaseQuery constructs a GetKnowledgeBaseQuery.
func NewGetKnowledgeBaseQuery(repo repository.CatalogRepository, cache repository.VersionedCache) *GetKnowledgeBaseQuery {
	return &GetKnowledgeBaseQuery{repo: repo, cache: cache}
}

// Execute retrieves a tenant-scoped Knowledge Base, enforcing causal freshness when min_version > 0.
func (q *GetKnowledgeBaseQuery) Execute(ctx context.Context, in dto.GetKBInput) (*dto.KBResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionGetKB); err != nil {
		return nil, err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return nil, fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}

	if q.cache != nil {
		cachedKB, err := q.cache.GetLatest(ctx, in.Caller.TenantID, kbID, in.MinVersion)
		if err == nil && cachedKB != nil && (in.MinVersion == 0 || cachedKB.Version >= in.MinVersion) {
			if cachedKB.State == entity.StateTombstoned {
				return nil, entity.ErrKnowledgeBaseNotFound
			}
			return &dto.KBResponse{KnowledgeBase: dto.CloneKB(cachedKB)}, nil
		}
	}

	kb, err := q.repo.GetByID(ctx, in.Caller.TenantID, kbID)
	if err != nil {
		return nil, err
	}

	if in.MinVersion > 0 && kb.Version < in.MinVersion {
		return nil, fmt.Errorf("%w: knowledge base version %d does not meet requested min_version %d", entity.ErrVersionConflict, kb.Version, in.MinVersion)
	}

	if q.cache != nil {
		_ = q.cache.PutVersion(ctx, kb)
	}

	return &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}, nil
}
