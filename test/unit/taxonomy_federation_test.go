package unit

import (
	"context"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
)

func TestTaxonomyTagValidationAndParsing(t *testing.T) {
	validTags := []struct {
		raw       string
		namespace string
		key       string
		value     string
	}{
		{"env:production", "env", "", "production"},
		{"compliance:hipaa", "compliance", "", "hipaa"},
		{"domain:finance", "domain", "", "finance"},
		{"org:team=search", "org", "team", "search"},
		{"compliance:tier=soc2", "compliance", "tier", "soc2"},
		{"geo:region=us-east-1", "geo", "region", "us-east-1"},
		{"system:model_version=v2.1", "system", "model_version", "v2.1"},
	}

	for _, tt := range validTags {
		parsed, err := entity.ParseTaxonomyTag(tt.raw)
		if err != nil {
			t.Fatalf("unexpected error parsing valid tag %q: %v", tt.raw, err)
		}
		if parsed.Namespace != tt.namespace {
			t.Errorf("expected namespace %q, got %q", tt.namespace, parsed.Namespace)
		}
		if parsed.Key != tt.key {
			t.Errorf("expected key %q, got %q", tt.key, parsed.Key)
		}
		if parsed.Value != tt.value {
			t.Errorf("expected value %q, got %q", tt.value, parsed.Value)
		}
	}

	invalidTags := []string{
		"",
		"   ",
		"env: production",
		"env :production",
		"env:production ",
		"no_colon_tag",
		":value",
		"namespace:",
		"ns:k=v=extra",
		"ns:k=",
		"ns:=v",
		"ns:a:b",
		"invalid space in ns:value",
		"ns:inv@lid_char",
		"ns:key=inv@lid",
		string(make([]byte, 129)), // > 128 characters
	}

	for _, raw := range invalidTags {
		_, err := entity.ParseTaxonomyTag(raw)
		if err == nil {
			t.Fatalf("expected error for invalid tag %q, got nil", raw)
		}
	}
}

func TestTaxonomyNormalizationAndDeduplication(t *testing.T) {
	tags := []string{
		"env:production",
		"domain:finance",
		"env:production", // duplicate
		"compliance:hipaa",
		"domain:finance", // duplicate
	}

	normalized, err := entity.NormalizeTaxonomyTags(tags)
	if err != nil {
		t.Fatalf("unexpected error normalizing tags: %v", err)
	}

	expected := []string{"compliance:hipaa", "domain:finance", "env:production"}
	if len(normalized) != len(expected) {
		t.Fatalf("expected %d normalized tags, got %d", len(expected), len(normalized))
	}
	for i, v := range expected {
		if normalized[i] != v {
			t.Errorf("at index %d: expected %q, got %q", i, v, normalized[i])
		}
	}
}

func TestTaxonomyQueryFederation(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)

	tenantID := "tenant-federation"
	caller := policy.CallerIdentity{TenantID: tenantID, UserID: "user-fed", Roles: []policy.Role{policy.RoleViewer}}

	now := time.Now().UTC()
	makeKB := func(id, name string, tags []string) *entity.KnowledgeBase {
		return &entity.KnowledgeBase{
			TenantID:           tenantID,
			KBID:               id,
			Name:               name,
			Version:            1,
			State:              entity.StateActive,
			RoutingPolicy:      entity.RoutingGreenOnly,
			ActiveIndexPointer: "index-" + id,
			ActiveSchema: entity.VectorSchema{
				EmbeddingModel: "bge-m3",
				Dimensions:     1024,
				DistanceMetric: entity.MetricCosine,
				SchemaVersion:  "1.0.0",
			},
			RetrievalProfile: entity.RetrievalProfile{DefaultTopK: 10, DenseWeight: 0.5, SparseWeight: 0.5},
			TaxonomyTags:     tags,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
	}

	// Corpus of Knowledge Bases
	kb1 := makeKB("0192e2fb-83bb-7000-8000-000000000001", "Production Finance PCI", []string{"compliance:pci", "domain:finance", "env:production"})
	kb2 := makeKB("0192e2fb-83bb-7000-8000-000000000002", "Production Health HIPAA", []string{"compliance:hipaa", "domain:healthcare", "env:production"})
	kb3 := makeKB("0192e2fb-83bb-7000-8000-000000000003", "Staging Finance", []string{"domain:finance", "env:staging"})
	kb4 := makeKB("0192e2fb-83bb-7000-8000-000000000004", "Production Finance SOC2", []string{"compliance:soc2", "domain:finance", "env:production"})

	for _, kb := range []*entity.KnowledgeBase{kb1, kb2, kb3, kb4} {
		if err := repo.Create(ctx, kb, nil, nil); err != nil {
			t.Fatalf("failed to insert kb %s: %v", kb.KBID, err)
		}
	}

	listQuery := query.NewListKnowledgeBasesQuery(repo)

	// Test 1: Single namespace filter `env:production` -> matches kb1, kb2, kb4 (3 entities)
	respProd, err := listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: []string{"env:production"},
		PageSize:        10,
	})
	if err != nil {
		t.Fatalf("ListKnowledgeBases failed: %v", err)
	}
	if len(respProd.KnowledgeBases) != 3 {
		t.Fatalf("expected 3 production KBs, got %d", len(respProd.KnowledgeBases))
	}

	// Test 2: Multi-namespace federation `env:production` AND `domain:finance` -> matches kb1, kb4 (2 entities)
	respProdFinance, err := listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: []string{"env:production", "domain:finance"},
		PageSize:        10,
	})
	if err != nil {
		t.Fatalf("ListKnowledgeBases multi-filter failed: %v", err)
	}
	if len(respProdFinance.KnowledgeBases) != 2 {
		t.Fatalf("expected 2 production finance KBs, got %d", len(respProdFinance.KnowledgeBases))
	}
	for _, kb := range respProdFinance.KnowledgeBases {
		if kb.KBID != kb1.KBID && kb.KBID != kb4.KBID {
			t.Errorf("unexpected KB %s in production finance results", kb.KBID)
		}
	}

	// Test 3: Specific compliance filter `compliance:hipaa` -> matches kb2 (1 entity)
	respHIPAA, err := listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: []string{"compliance:hipaa"},
		PageSize:        10,
	})
	if err != nil {
		t.Fatalf("ListKnowledgeBases HIPAA filter failed: %v", err)
	}
	if len(respHIPAA.KnowledgeBases) != 1 || respHIPAA.KnowledgeBases[0].KBID != kb2.KBID {
		t.Fatalf("expected kb2 for HIPAA filter, got %v", respHIPAA.KnowledgeBases)
	}

	// Test 4: Unmatched filter -> returns 0 items
	respNone, err := listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: []string{"domain:marketing"},
		PageSize:        10,
	})
	if err != nil {
		t.Fatalf("ListKnowledgeBases empty filter failed: %v", err)
	}
	if len(respNone.KnowledgeBases) != 0 {
		t.Fatalf("expected 0 results for non-existent domain, got %d", len(respNone.KnowledgeBases))
	}

	// Test 5: Invalid filter token rejected before database query
	_, err = listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: []string{"invalid_no_colon"},
		PageSize:        10,
	})
	if err == nil {
		t.Fatalf("expected error for invalid filter token, got nil")
	}
}
