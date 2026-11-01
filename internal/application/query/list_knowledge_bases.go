package query

import (
	"context"
	"fmt"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// ListKnowledgeBasesQuery executes paginated tenant-scoped discovery filtered by hierarchical taxonomy tags.
type ListKnowledgeBasesQuery struct {
	repo repository.CatalogRepository
}

// NewListKnowledgeBasesQuery constructs a ListKnowledgeBasesQuery.
func NewListKnowledgeBasesQuery(repo repository.CatalogRepository) *ListKnowledgeBasesQuery {
	return &ListKnowledgeBasesQuery{repo: repo}
}

// Execute validates taxonomy filter tokens and pagination bounds before querying the catalog repository.
func (q *ListKnowledgeBasesQuery) Execute(ctx context.Context, in dto.ListKBsInput) (*dto.ListKBsResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionListKBs); err != nil {
		return nil, err
	}

	normalizedFilters, err := entity.NormalizeTaxonomyTags(in.TaxonomyFilters)
	if err != nil {
		return nil, err
	}

	pageSize := in.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if pageSize < 1 || pageSize > maxPageSize {
		return nil, fmt.Errorf("%w: page_size must be between 1 and %d", entity.ErrInvalidInput, maxPageSize)
	}

	items, nextToken, err := q.repo.List(ctx, repository.ListFilter{
		TenantID:        in.Caller.TenantID,
		TaxonomyFilters: normalizedFilters,
		PageSize:        pageSize,
		PageToken:       in.PageToken,
	})
	if err != nil {
		return nil, err
	}

	cloned := make([]*entity.KnowledgeBase, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, dto.CloneKB(item))
	}

	return &dto.ListKBsResponse{
		KnowledgeBases: cloned,
		NextPageToken:  nextToken,
	}, nil
}
