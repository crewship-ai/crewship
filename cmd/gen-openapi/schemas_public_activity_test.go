package main

import "testing"

func TestPublicActivityCatalogUsesConcreteHandlerShapes(t *testing.T) {
	catalog := publicActivitySchemaCatalog()
	want := map[string]string{
		"GET /api/v1/agents/{agentId}/chats":                     "array",
		"GET /api/v1/inbox":                                      "rows",
		"GET /api/v1/notifications":                              "array",
		"GET /api/v1/audit":                                      "data",
		"GET /api/v1/workspaces/{workspaceId}/pipeline-webhooks": "array",
	}
	for route, marker := range want {
		var schema map[string]any
		for _, domain := range catalog {
			if entry, ok := domain[route]; ok {
				schema = entry.Response
				break
			}
		}
		if schema == nil {
			t.Fatalf("missing public activity route %q", route)
		}
		if marker == "array" && schema["type"] != "array" {
			t.Errorf("%s response = %#v, want array", route, schema)
		}
		if marker != "array" {
			props := schema["properties"].(map[string]any)
			if _, ok := props[marker]; !ok {
				t.Errorf("%s response missing %q: %#v", route, marker, props)
			}
		}
	}
}

func TestPublicActivityRequestSchemasExposeActualFields(t *testing.T) {
	catalog := publicActivitySchemaCatalog()
	checks := map[string][]string{
		"POST /api/v1/conversations/search":                       {"agent_id", "query", "limit"},
		"PATCH /api/v1/inbox/{id}":                                {"state", "resolved_action"},
		"POST /api/v1/workspaces/{workspaceId}/pipeline-webhooks": {"name", "target_pipeline_slug", "inputs_template"},
	}
	for route, fields := range checks {
		var schema map[string]any
		for _, domain := range catalog {
			if entry, ok := domain[route]; ok {
				schema = entry.Request
				break
			}
		}
		if schema == nil {
			t.Fatalf("missing request schema for %q", route)
		}
		props := schema["properties"].(map[string]any)
		for _, field := range fields {
			if _, ok := props[field]; !ok {
				t.Errorf("%s request missing %q", route, field)
			}
		}
	}
}

func TestPublicActivityCatalogIsWiredIntoGenerator(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/audit"}})
	op := doc["paths"].(map[string]any)["/api/v1/audit"].(map[string]any)["get"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	content := response["content"].(map[string]any)["application/json"].(map[string]any)
	schema := content["schema"].(map[string]any)
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("audit response was not wired to a concrete object schema: %#v", schema)
	}
	if _, ok := props["pagination"]; !ok {
		t.Fatalf("audit response is missing pagination: %#v", props)
	}
}
