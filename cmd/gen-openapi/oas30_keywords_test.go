package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// oas31OnlyKeywords are JSON Schema / OpenAPI 3.1 keywords that the 3.0.3
// Schema Object does not define; strict 3.0 validators reject the document
// when one appears in a schema (#4630).
var oas31OnlyKeywords = map[string]bool{
	"propertyNames": true, "patternProperties": true, "const": true,
	"$schema": true, "$id": true, "$anchor": true, "$defs": true, "$dynamicRef": true, "$dynamicAnchor": true,
	"unevaluatedProperties": true, "unevaluatedItems": true,
	"dependentSchemas": true, "dependentRequired": true,
	"prefixItems": true, "contains": true, "minContains": true, "maxContains": true,
	"if": true, "then": true, "else": true, "contentMediaType": true, "contentEncoding": true,
}

// Maps whose keys are user-chosen names, not schema keywords.
var oasNameMaps = map[string]bool{"properties": true, "schemas": true, "paths": true, "responses": true, "content": true, "headers": true, "parameters": true, "examples": true, "example": true, "enum": true, "default": true, "x-crewship-property-names": false}

func TestGeneratedDocumentHasNoOAS31OnlyKeywords(t *testing.T) {
	var doc any
	if err := json.Unmarshal(specBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	var found []string
	var walk func(v any, pointer string, namesMap bool)
	walk = func(v any, pointer string, namesMap bool) {
		switch typed := v.(type) {
		case map[string]any:
			for key, child := range typed {
				if !namesMap && oas31OnlyKeywords[key] {
					found = append(found, pointer+"/"+key)
				}
				if !namesMap && key == "type" {
					if _, isArray := child.([]any); isArray {
						found = append(found, pointer+"/type (array)")
					}
				}
				if key == "example" || key == "default" || key == "enum" {
					continue // literal values, not schemas
				}
				walk(child, pointer+"/"+strings.ReplaceAll(key, "/", "~1"), !namesMap && oasNameMaps[key])
			}
		case []any:
			for _, child := range typed {
				walk(child, pointer+"/[]", false)
			}
		}
	}
	walk(doc, "", false)
	sort.Strings(found)
	if len(found) != 0 {
		t.Fatalf("OpenAPI 3.0.3 document uses %d 3.1-only keywords: %v", len(found), found)
	}
}

// The decision value maps keep their key pattern as a vendor extension, since
// 3.0 has no propertyNames, and the document stays loadable by a 3.0 validator.
func TestDecisionValueMapsKeepKeyPattern(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(specBytes(t), &doc); err != nil {
		t.Fatal(err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	values := schemas["ChatDecisionAnswer"].(map[string]any)["properties"].(map[string]any)["values"].(map[string]any)
	ext, _ := values["x-crewship-property-names"].(map[string]any)
	if ext["pattern"] != `^[A-Za-z][A-Za-z0-9_-]{0,63}$` {
		t.Fatalf("key pattern lost: %#v", values)
	}
	if _, ok := values["additionalProperties"].(map[string]any); !ok {
		t.Fatalf("value constraints lost: %#v", values)
	}
	loaded, err := openapi3.NewLoader().LoadFromData(specBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Components.Schemas["ChatDecisionAnswer"].Value.Validate(context.Background()); err != nil {
		t.Fatalf("ChatDecisionAnswer is not valid OpenAPI 3.0: %v", err)
	}
}
