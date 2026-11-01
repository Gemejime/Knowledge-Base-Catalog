package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

// PromotePointerCommand orchestrates atomic Blue-Green index pointer promotion gated on zero authoritative delta lag (ADR-002).
type PromotePointerCommand struct {
	repo  repository.CatalogRepository
	cache repository.VersionedCache
}

// NewPromotePointerCommand constructs a PromotePointerCommand.
func NewPromotePointerCommand(repo repository.CatalogRepository, cache repository.VersionedCache) *PromotePointerCommand {
	return &PromotePointerCommand{repo: repo, cache: cache}
}

// Execute verifies RBAC and promotes the Blue candidate pointer to active when authoritative delta_lag_records == 0.
func (c *PromotePointerCommand) Execute(ctx context.Context, in dto.PromotePointerInput) (*dto.KBResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionPromotePointer); err != nil {
		return nil, err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return nil, fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}
	if in.ExpectedVersion == 0 {
		return nil, fmt.Errorf("%w: expected_version must be >= 1", entity.ErrInvalidInput)
	}

	now := time.Now().UTC()
	kb, err := c.repo.PromotePointer(ctx, in.Caller.TenantID, kbID, in.ExpectedVersion, in.VerifiedDeltaLagRecords, now)
	if err != nil {
		return nil, err
	}

	if c.cache != nil {
		_ = c.cache.PutVersion(ctx, kb)
	}

	return &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}, nil
}
