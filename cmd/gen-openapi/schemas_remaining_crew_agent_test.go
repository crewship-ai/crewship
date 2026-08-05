package main

import "testing"

func TestRemainingCrewAgentCatalogUsesConcreteContracts(t *testing.T) {
	routes, components := remainingCrewAgentSchemaCatalogV1()
	if len(routes) < 45 {
		t.Fatalf("remaining crew/agent catalog has %d routes, want at least 45", len(routes))
	}
	for route, contract := range routes {
		if contract.Response == nil {
			t.Fatalf("%s has no response schema", route)
		}
		name := contract.Response["$ref"].(string)[len("#/components/schemas/"):]
		schema, ok := components[name].(map[string]any)
		if !ok {
			t.Fatalf("%s references missing component %q", route, name)
		}
		if schema["type"] == "object" && len(schema) == 1 {
			t.Fatalf("%s component %q is generic", route, name)
		}
	}
}

func TestRemainingCrewAgentCatalogIsWired(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/agents/{agentId}/learning"},
		{method: "POST", path: "/api/v1/agents/hire"},
		{method: "PUT", path: "/api/v1/crews/{crewId}/policy"},
	})
	paths := doc["paths"].(map[string]any)
	for path, method := range map[string]string{
		"/api/v1/agents/{agentId}/learning": "get",
		"/api/v1/agents/hire":               "post",
		"/api/v1/crews/{crewId}/policy":     "put",
	} {
		op := paths[path].(map[string]any)[method].(map[string]any)
		response := op["responses"].(map[string]any)["200"].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if schema["$ref"] == nil {
			t.Errorf("%s %s response is not a component reference: %#v", method, path, schema)
		}
	}
	request := paths["/api/v1/agents/hire"].(map[string]any)["post"].(map[string]any)["requestBody"].(map[string]any)
	requestSchema := request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if requestSchema["$ref"] == nil {
		t.Fatalf("hire request is not a concrete component reference: %#v", requestSchema)
	}
}
