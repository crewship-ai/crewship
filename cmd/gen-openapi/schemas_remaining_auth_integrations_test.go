package main

import "testing"

func TestRemainingAuthIntegrationsCatalogAuditsHandlerSurfaces(t *testing.T) {
	routes, components := remainingAuthIntegrationsSchemaCatalog()
	for _, path := range []string{
		"POST /api/v1/feedback", "GET /api/v1/hooks", "POST /api/v1/hooks/{id}/enable",
		"POST /api/v1/labels", "PATCH /api/v1/saved-views/{viewId}",
		"GET /api/v1/auth/sessions", "POST /api/v1/auth/pair/start",
		"GET /api/v1/credentials/{credentialId}/audit",
	} {
		contract, ok := routes[path]
		if !ok || (contract.Request == nil && contract.Response == nil) {
			t.Fatalf("missing concrete contract for %s", path)
		}
	}
	for name, schema := range components {
		if schemaMap, ok := schema.(map[string]any); !ok || (schemaMap["type"] == "object" && len(schemaMap) == 1) {
			t.Errorf("component %q is generic or malformed: %#v", name, schema)
		}
	}
}

func TestRemainingAuthIntegrationsCatalogWiresExactFields(t *testing.T) {
	routes, components := remainingAuthIntegrationsSchemaCatalog()
	feedback := components["RemainingFeedbackCreateRequest"].(map[string]any)
	if got := feedback["required"]; got == nil {
		t.Fatal("feedback create must require message_id and signal")
	}
	if got := routes["GET /api/v1/hooks"].Response["properties"].(map[string]any)["rows"]; got == nil {
		t.Fatal("hooks list must expose rows envelope")
	}
	if got := routes["GET /api/v1/notifications/count"].Response["properties"].(map[string]any)["unread"]; got == nil {
		t.Fatal("notification count must expose unread")
	}
}

func TestRemainingAuthIntegrationsCatalogIsWiredIntoDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/feedback"}, {method: "GET", path: "/api/v1/saved-views"}})
	paths := doc["paths"].(map[string]any)
	feedback := paths["/api/v1/feedback"].(map[string]any)["post"].(map[string]any)
	body := feedback["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["$ref"] != "#/components/schemas/RemainingFeedbackCreateRequest" {
		t.Fatalf("feedback request schema = %#v", body)
	}
	view := paths["/api/v1/saved-views"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if view["type"] != "array" {
		t.Fatalf("saved views response schema = %#v", view)
	}
}
