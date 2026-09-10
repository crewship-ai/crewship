package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPageProjectSchemaRequiresSourceAndRevision(t *testing.T) {
	entry := routeSchemaCatalog()["PUT /api/v1/pages/{slug}/project"]
	if !entry.RequestRequired || entry.Request["additionalProperties"] != false {
		t.Fatal("source write is not a strict required body")
	}
	required := entry.Request["required"].([]string)
	if len(required) != 2 || required[0] != "expected_revision" || required[1] != "project" {
		t.Fatal("missing CAS/source contract")
	}
	if routeSchemaCatalog()["GET /api/v1/pages/{slug}/project"].Response == nil {
		t.Fatal("missing read schema")
	}
	export := routeSchemaCatalog()["GET /api/v1/pages/{slug}/export"].Response
	if export["properties"].(map[string]any)["project"] == nil {
		t.Fatal("export schema would drop source")
	}
}

func TestPageApplicationConditionalResponseHasNoBody(t *testing.T) {
	data, err := os.ReadFile("../../internal/api/openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	response := spec["paths"].(map[string]any)["/api/v1/pages/{slug}/application"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["304"].(map[string]any)
	if _, exists := response["content"]; exists {
		t.Fatal("304 cannot describe a response body")
	}
}
