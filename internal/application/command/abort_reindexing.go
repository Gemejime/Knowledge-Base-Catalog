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

// AbortReindexingCommand aborts an active Blue-Green migration, keeps the Green pointer locked,
// resets routing_policy to GREEN_ONLY, and emits a ReindexAborted cleanup event to outbox_events.
type AbortReindexingCommand struct {
	repo  repository.CatalogRepository
	cache repository.VersionedCache
}

// NewAbortReindexingCommand constructs an AbortReindexingCommand.
func NewAbortReindexingCommand(repo repository.CatalogRepository, cache repository.VersionedCache) *AbortReindexingCommand {
	return &AbortReindexingCommand{repo: repo, cache: cache}
}

// Execute aborts the migration and returns (*dto.KBResponse, error) to support both REST (200 OK KBResponse) and gRPC (Empty).
func (c *AbortReindexingCommand) Execute(ctx context.Context, in dto.AbortReindexingInput) (*dto.KBResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionAbortReindexing); err != nil {
		return nil, err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return nil, fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}
	if in.ExpectedVersion == 0 {
		return nil, fmt.Errorf("%w: expected_version must be >= 1", entity.ErrInvalidInput)
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: abort reason is required", entity.ErrInvalidInput)
	}

	now := time.Now().UTC()
	kb, err := c.repo.AbortReindexing(ctx, in.Caller.TenantID, kbID, in.ExpectedVersion, reason, now)
	if err != nil {
		return nil, err
	}

	if c.cache != nil {
		_ = c.cache.PutVersion(ctx, kb)
	}

	return &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}, nil
}
