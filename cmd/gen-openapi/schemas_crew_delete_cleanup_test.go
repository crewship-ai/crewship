package main

import "testing"

// The crew DELETE response was once overwritten by a later catalog and lost
// its cleanup and sidecar_teardown fields. Guard the assembled document, not
// one catalog, so any later override is caught.
func TestCrewDeleteResponseKeepsCleanupInFinalDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "DELETE", path: "/api/v1/crews/{crewId}"}})
	op := doc["paths"].(map[string]any)["/api/v1/crews/{crewId}"].(map[string]any)["delete"].(map[string]any)
	schema := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if ref, ok := schema["$ref"].(string); ok {
		schema = doc["components"].(map[string]any)["schemas"].(map[string]any)[ref[len("#/components/schemas/"):]].(map[string]any)
	}
	props := schema["properties"].(map[string]any)
	for _, field := range []string{"success", "cleanup", "sidecar_teardown"} {
		if _, ok := props[field]; !ok {
			t.Errorf("crew DELETE response lacks %q: %#v", field, props)
		}
	}
	cleanup := props["cleanup"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"state", "complete", "remaining"} {
		if _, ok := cleanup[field]; !ok {
			t.Errorf("cleanup schema lacks %q", field)
		}
	}
}
