package main

import "testing"

func TestFinalSpecialDomainSchemaCatalogCoversRequestedRoutes(t *testing.T) {
	catalog := finalSpecialDomainSchemaCatalog()
	for _, route := range []string{
		"POST /api/v1/consolidate/run", "GET /api/v1/eval/runs", "GET /api/v1/crew-connections",
		"GET /api/v1/crew-templates", "GET /api/v1/mcp-registry", "GET /api/v1/mcp-tool-calls",
		"GET /api/v1/skills/proposed", "GET /api/v1/templates", "GET /api/v1/workflow-templates",
		"POST /api/v1/crew-ai-suggest", "POST /api/v1/memory/search/hybrid",
	} {
		if _, ok := catalog[route]; !ok {
			t.Errorf("catalog missing %q", route)
		}
	}
}

func TestFinalSpecialDomainSchemaCatalogWiresExactShapes(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/mcp-registry"},
		{method: "GET", path: "/api/v1/mcp-tool-calls"},
		{method: "POST", path: "/api/v1/crew-ai-suggest"},
		{method: "POST", path: "/api/v1/memory/search/hybrid"},
	})
	paths := doc["paths"].(map[string]any)
	registry := responseSchemaFromOperation(t, paths["/api/v1/mcp-registry"].(map[string]any)["get"].(map[string]any))
	if registry["properties"].(map[string]any)["servers"] == nil {
		t.Fatal("registry response lacks servers")
	}
	toolCalls := responseSchemaFromOperation(t, paths["/api/v1/mcp-tool-calls"].(map[string]any)["get"].(map[string]any))
	if toolCalls["type"] != "array" {
		t.Fatalf("tool calls schema = %#v", toolCalls)
	}
	ai := responseSchemaFromOperation(t, paths["/api/v1/crew-ai-suggest"].(map[string]any)["post"].(map[string]any))
	if ai["properties"].(map[string]any)["agents"] == nil {
		t.Fatal("AI suggestion response lacks agents")
	}
	memory := responseSchemaFromOperation(t, paths["/api/v1/memory/search/hybrid"].(map[string]any)["post"].(map[string]any))
	if memory["properties"].(map[string]any)["hits"] == nil {
		t.Fatal("memory response lacks hits")
	}
}

func TestFinalSpecialDomainSchemaCatalogReturnsFreshMap(t *testing.T) {
	one := finalSpecialDomainSchemaCatalog()
	delete(one, "GET /api/v1/mcp-registry")
	if _, ok := finalSpecialDomainSchemaCatalog()["GET /api/v1/mcp-registry"]; !ok {
		t.Fatal("catalog returned shared map")
	}
}
