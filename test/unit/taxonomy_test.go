package unit_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

func TestTaxonomyParsingAndNormalization(t *testing.T) {
	valid := []struct {
		raw       string
		namespace string
		key       string
		value     string
	}{
		{raw: "env:production", namespace: "env", key: "", value: "production"},
		{raw: "compliance:hipaa", namespace: "compliance", key: "", value: "hipaa"},
		{raw: "domain:finance", namespace: "domain", key: "", value: "finance"},
		{raw: "compliance:tier=hipaa", namespace: "compliance", key: "tier", value: "hipaa"},
		{raw: "org:business_unit=core-ai.v1", namespace: "org", key: "business_unit", value: "core-ai.v1"},
	}

	for _, tc := range valid {
		parsed, err := entity.ParseTaxonomyTag(tc.raw)
		if err != nil {
			t.Fatalf("expected %q to parse cleanly, got error: %v", tc.raw, err)
		}
		if parsed.Namespace != tc.namespace || parsed.Key != tc.key || parsed.Value != tc.value {
			t.Fatalf("unexpected parsed fields for %q: %+v", tc.raw, parsed)
		}
	}

	malformed := []string{
		"",
		"   ",
		" env:production",
		"env:production ",
		"nocolon",
		":missingnamespace",
		"missingvalue:",
		"env:too:many:colons",
		"env:=missingkey",
		"env:missingval=",
		"env:k=v=extra",
		"-badstart:val",
		"env:$invalidchar",
		"env:val with spaces",
	}

	for _, bad := range malformed {
		if _, err := entity.ParseTaxonomyTag(bad); !errors.Is(err, entity.ErrInvalidInput) {
			t.Fatalf("expected ErrInvalidInput for malformed taxonomy tag %q, got %v", bad, err)
		}
	}

	normalized, err := entity.NormalizeTaxonomyTags([]string{
		"domain:finance",
		"env:production",
		"compliance:hipaa",
		"env:production", // duplicate
	})
	if err != nil {
		t.Fatalf("NormalizeTaxonomyTags failed: %v", err)
	}
	expected := []string{"compliance:hipaa", "domain:finance", "env:production"}
	if !reflect.DeepEqual(normalized, expected) {
		t.Fatalf("expected deduplicated sorted tags %v, got %v", expected, normalized)
	}
}
