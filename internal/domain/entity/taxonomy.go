package entity

import (
	"fmt"
	"sort"
	"strings"
)

// TaxonomyTag represents a parsed hierarchical namespace tag ("namespace:key=value" or "namespace:value").
type TaxonomyTag struct {
	Namespace string
	Key       string
	Value     string
	Raw       string
}

// ParseTaxonomyTag validates and parses a single hierarchical taxonomy token.
// Accepted formats:
//   - "namespace:value" (e.g., "env:production", "compliance:hipaa", "domain:finance")
//   - "namespace:key=value" (e.g., "compliance:tier=hipaa", "org:unit=research")
func ParseTaxonomyTag(raw string) (TaxonomyTag, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return TaxonomyTag{}, fmt.Errorf("%w: taxonomy tag %q must not be empty or contain leading/trailing whitespace", ErrInvalidInput, raw)
	}
	if len(raw) > 128 {
		return TaxonomyTag{}, fmt.Errorf("%w: taxonomy tag exceeds maximum length of 128 characters", ErrInvalidInput)
	}

	colonIdx := strings.IndexByte(raw, ':')
	if colonIdx <= 0 || colonIdx == len(raw)-1 {
		return TaxonomyTag{}, fmt.Errorf("%w: taxonomy tag %q must follow 'namespace:value' or 'namespace:key=value' format", ErrInvalidInput, raw)
	}

	namespace := raw[:colonIdx]
	rest := raw[colonIdx+1:]

	if strings.IndexByte(rest, ':') != -1 {
		return TaxonomyTag{}, fmt.Errorf("%w: taxonomy tag %q contains multiple ':' delimiters", ErrInvalidInput, raw)
	}
	if !isValidTaxonomyToken(namespace) {
		return TaxonomyTag{}, fmt.Errorf("%w: invalid taxonomy namespace %q", ErrInvalidInput, namespace)
	}

	var key, value string
	eqIdx := strings.IndexByte(rest, '=')
	if eqIdx != -1 {
		if eqIdx == 0 || eqIdx == len(rest)-1 {
			return TaxonomyTag{}, fmt.Errorf("%w: malformed key=value in taxonomy tag %q", ErrInvalidInput, raw)
		}
		key = rest[:eqIdx]
		value = rest[eqIdx+1:]
		if strings.IndexByte(value, '=') != -1 {
			return TaxonomyTag{}, fmt.Errorf("%w: taxonomy tag %q contains multiple '=' delimiters", ErrInvalidInput, raw)
		}
		if !isValidTaxonomyToken(key) || !isValidTaxonomyToken(value) {
			return TaxonomyTag{}, fmt.Errorf("%w: invalid key or value token in taxonomy tag %q", ErrInvalidInput, raw)
		}
	} else {
		value = rest
		if !isValidTaxonomyToken(value) {
			return TaxonomyTag{}, fmt.Errorf("%w: invalid value token in taxonomy tag %q", ErrInvalidInput, raw)
		}
	}

	return TaxonomyTag{
		Namespace: namespace,
		Key:       key,
		Value:     value,
		Raw:       raw,
	}, nil
}

// NormalizeTaxonomyTags validates, deduplicates, and deterministically sorts a slice of taxonomy tags.
func NormalizeTaxonomyTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return []string{}, nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		parsed, err := ParseTaxonomyTag(t)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[parsed.Raw]; !exists {
			seen[parsed.Raw] = struct{}{}
			out = append(out, parsed.Raw)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isValidTaxonomyToken(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLower := c >= 'a' && c <= 'z'
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		isAllowedSymbol := c == '-' || c == '_' || c == '.'
		if !isLower && !isUpper && !isDigit && !isAllowedSymbol {
			return false
		}
	}
	// First character must be alphanumeric.
	first := s[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')
}
