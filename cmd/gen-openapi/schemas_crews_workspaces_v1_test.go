package main

import "testing"

func TestCrewWorkspaceGETSchemaCatalogV1CoversPriorGenericRoutes(t *testing.T) {
	catalog, components := crewWorkspaceGETSchemaCatalogV1()
	routes := catalog["crew-workspace-get"]
	if got := len(routes); got != 42 {
		t.Fatalf("audited crew/workspace GET route count = %d, want 42", got)
	}
	for key, contract := range routes {
		if contract.Response == nil || contract.Response["$ref"] == nil {
			t.Fatalf("%s has no component response reference", key)
		}
		name := contract.Response["$ref"].(string)[len("#/components/schemas/"):]
		schema, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("%s references missing component %q", key, name)
		}
		if schema["type"] == "object" && len(schema) == 1 {
			t.Fatalf("%s component %q is a generic object", key, name)
		}
	}
}

func TestBuildDocumentUsesCrewWorkspaceGETComponents(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/crews/{crewId}/capabilities"},
		{method: "GET", path: "/api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget"},
	})
	paths := doc["paths"].(map[string]any)
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for path, want := range map[string]string{
		"/api/v1/crews/{crewId}/capabilities":                      "CrewCapabilitiesResponseV1",
		"/api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget": "WorkspacePipelineBudgetResponseV1",
	} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		response := op["responses"].(map[string]any)["200"].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if got := schema["$ref"]; got != "#/components/schemas/"+want {
			t.Errorf("%s response ref = %v, want %s", path, got, want)
		}
		if _, ok := components[want]; !ok {
			t.Errorf("missing component %q", want)
		}
	}
}
