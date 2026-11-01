package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
)

const idempotencyTTL = 24 * time.Hour

// CreateKnowledgeBaseCommand orchestrates Knowledge Base creation, RBAC checks, idempotency deduplication, and outbox emission.
type CreateKnowledgeBaseCommand struct {
	repo  repository.CatalogRepository
	cache repository.VersionedCache
}

// NewCreateKnowledgeBaseCommand constructs a CreateKnowledgeBaseCommand.
func NewCreateKnowledgeBaseCommand(repo repository.CatalogRepository, cache repository.VersionedCache) *CreateKnowledgeBaseCommand {
	return &CreateKnowledgeBaseCommand{repo: repo, cache: cache}
}

// Execute validates and persists a new Knowledge Base atomically alongside its KBCreated outbox event.
func (c *CreateKnowledgeBaseCommand) Execute(ctx context.Context, in dto.CreateKBInput) (*dto.KBResponse, error) {
	if err := policy.EvaluateAccess(in.Caller, in.Caller.TenantID, policy.ActionCreateKB); err != nil {
		return nil, err
	}

	idempKey, err := resolveIdempotencyKey(in.HeaderIdempKey, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var reqHash string
	if idempKey != "" {
		reqHash, err = computeCreateRequestHash(in.Caller.TenantID, in)
		if err != nil {
			return nil, err
		}
		existing, err := c.repo.GetIdempotencyRecord(ctx, in.Caller.TenantID, idempKey, now)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.RequestHash != reqHash {
				return nil, fmt.Errorf("%w: idempotency key %q reused with different request parameters", entity.ErrIdempotencyKeyConflict, idempKey)
			}
			var cachedResp dto.KBResponse
			if err := json.Unmarshal(existing.ResponsePayload, &cachedResp); err != nil {
				return nil, fmt.Errorf("failed to decode cached idempotency response: %w", err)
			}
			return &cachedResp, nil
		}
	}

	kb, err := entity.NewKnowledgeBase(
		in.Caller.TenantID,
		"",
		in.Name,
		in.Description,
		in.VectorSchema,
		in.RetrievalProfile,
		in.TaxonomyTags,
		now,
	)
	if err != nil {
		return nil, err
	}

	outboxEvt, err := event.NewOutboxEvent(kb, event.EventKBCreated, nil, now)
	if err != nil {
		return nil, err
	}

	resp := &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}
	var idempRecord *repository.IdempotencyRecord
	if idempKey != "" {
		payloadBytes, err := json.Marshal(resp)
		if err != nil {
			return nil, err
		}
		idempRecord = &repository.IdempotencyRecord{
			TenantID:        in.Caller.TenantID,
			IdempotencyKey:  idempKey,
			Operation:       string(policy.ActionCreateKB),
			RequestHash:     reqHash,
			ResponsePayload: payloadBytes,
			StatusCode:      http.StatusCreated,
			CreatedAt:       now,
			ExpiresAt:       now.Add(idempotencyTTL),
		}
	}

	if err := c.repo.Create(ctx, kb, outboxEvt, idempRecord); err != nil {
		return nil, err
	}

	if c.cache != nil {
		_ = c.cache.PutVersion(ctx, kb)
	}

	return &dto.KBResponse{KnowledgeBase: dto.CloneKB(kb)}, nil
}

func resolveIdempotencyKey(headerKey, bodyKey string) (string, error) {
	h := strings.TrimSpace(headerKey)
	b := strings.TrimSpace(bodyKey)
	if h != "" && b != "" && h != b {
		return "", fmt.Errorf("%w: Idempotency-Key header (%q) conflicts with body idempotency_key (%q)", entity.ErrInvalidInput, h, b)
	}
	if h != "" {
		return h, nil
	}
	return b, nil
}

func computeCreateRequestHash(tenantID string, in dto.CreateKBInput) (string, error) {
	normalizedTags, err := entity.NormalizeTaxonomyTags(in.TaxonomyTags)
	if err != nil {
		return "", err
	}
	canonical := struct {
		TenantID         string                  `json:"tenant_id"`
		Name             string                  `json:"name"`
		Description      string                  `json:"description"`
		VectorSchema     entity.VectorSchema     `json:"vector_schema"`
		RetrievalProfile entity.RetrievalProfile `json:"retrieval_profile"`
		TaxonomyTags     []string                `json:"taxonomy_tags"`
	}{
		TenantID:         strings.TrimSpace(tenantID),
		Name:             strings.TrimSpace(in.Name),
		Description:      strings.TrimSpace(in.Description),
		VectorSchema:     in.VectorSchema,
		RetrievalProfile: in.RetrievalProfile,
		TaxonomyTags:     normalizedTags,
	}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
