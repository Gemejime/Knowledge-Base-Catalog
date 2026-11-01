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

// DeleteKnowledgeBaseCommand soft-deletes (TOMBSTONED) a Knowledge Base under OCC and emits KBDeleted to outbox_events.
type DeleteKnowledgeBaseCommand struct {
	repo repository.CatalogRepository
}

// NewDeleteKnowledgeBaseCommand constructs a DeleteKnowledgeBaseCommand.
func NewDeleteKnowledgeBaseCommand(repo repository.CatalogRepository) *DeleteKnowledgeBaseCommand {
	return &DeleteKnowledgeBaseCommand{repo: repo}
}

// Execute tombstones the Knowledge Base and writes the purge event to the transactional outbox.
func (c *DeleteKnowledgeBaseCommand) Execute(ctx context.Context, in dto.DeleteKBInput) error {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionDeleteKB); err != nil {
		return err
	}
	kbID := strings.TrimSpace(in.KBID)
	if kbID == "" {
		return fmt.Errorf("%w: kb_id is required", entity.ErrInvalidInput)
	}
	if in.ExpectedVersion == 0 {
		return fmt.Errorf("%w: expected_version must be >= 1", entity.ErrInvalidInput)
	}

	now := time.Now().UTC()
	return c.repo.SoftDelete(ctx, in.Caller.TenantID, kbID, in.ExpectedVersion, now)
}
