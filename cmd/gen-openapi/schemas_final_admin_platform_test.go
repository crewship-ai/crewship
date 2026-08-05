package main

import "testing"

func TestFinalAdminPlatformCatalogAuditsRequestedRoutes(t *testing.T) {
	routes, components := finalAdminPlatformSchemaCatalog()
	want := []string{
		"GET /api/v1/admin/keeper/governance", "GET /api/v1/auth/google/status", "POST /api/v1/bootstrap",
		"GET /api/v1/onboarding/status", "GET /api/v1/features/catalog", "GET /api/v1/models",
		"GET /api/v1/instance/settings", "GET /api/v1/oauth/providers", "GET /api/v1/ws-token",
	}
	for _, route := range want {
		if routes[route].Response == nil {
			t.Errorf("%s has no audited response", route)
		}
	}
	for name, raw := range components {
		schema := raw.(map[string]any)
		if schema["type"] == "object" && schema["additionalProperties"] == true {
			t.Errorf("%s remains an unconstrained object", name)
		}
	}
}

func TestFinalAdminPlatformCatalogWiresIntoDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/models", source: "internal/api/router_crews.go", call: "r.mux.Handle"}})
	paths := doc["paths"].(map[string]any)
	op := paths["/api/v1/models"].(map[string]any)["get"].(map[string]any)
	schema := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("models response schema = %#v, want object", schema)
	}
	if schema["properties"].(map[string]any)["models"] == nil {
		t.Fatal("models response is missing models property")
	}
}
