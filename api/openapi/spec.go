package openapi

import (
	_ "embed"
)

//go:embed openapi.json
var specJSON []byte

// SpecJSON returns the documented OpenAPI 3.1 contract as JSON bytes.
func SpecJSON() []byte {
	return specJSON
}

