package main

import "testing"

func TestSharedCrewReadsHaveSeparateClosedContracts(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/crews"}, {method: "GET", path: "/api/v1/crews/{crewId}/access/me"}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	shared, ok := schemas["SharedCrew"].(map[string]any)
	if !ok {
		t.Fatal("missing safe crew projection")
	}
	if shared["additionalProperties"] != false {
		t.Fatal("shared crew contract must be closed")
	}
	props := shared["properties"].(map[string]any)
	for _, hidden := range []string{"description", "mcp_config_json", "container_memory_mb", "members", "credentials"} {
		if props[hidden] != nil {
			t.Fatal("private field in shared crew", hidden)
		}
	}
	read := schemas["CrewRead"].(map[string]any)
	if len(read["oneOf"].([]any)) != 2 {
		t.Fatal("must retain trusted and restricted projections separately")
	}
	paths := doc["paths"].(map[string]any)
	access := paths["/api/v1/crews/{crewId}/access/me"].(map[string]any)["get"].(map[string]any)
	if responseSchemaFromOperation(t, access)["$ref"] != "#/components/schemas/CrewAccessMe" {
		t.Fatal("own access is not typed")
	}
}
