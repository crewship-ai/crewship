package main

import "testing"

func TestBuildDocumentUsesAuditedReadSchemas(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/agents"},
		{method: "GET", path: "/api/v1/runs"},
		{method: "GET", path: "/api/v1/agents/{agentId}"},
		{method: "POST", path: "/api/v1/agents"},
	})

	paths := doc["paths"].(map[string]any)
	assertResponseRef(t, paths, "/api/v1/agents", "AgentList")
	assertResponseRef(t, paths, "/api/v1/runs", "RunList")
	assertResponseRef(t, paths, "/api/v1/agents/{agentId}", "Agent")

	post := paths["/api/v1/agents"].(map[string]any)["post"].(map[string]any)
	request := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if request.(map[string]any)["type"] != "object" {
		t.Fatalf("request schema = %#v, want generic object fallback", request)
	}
}

func assertResponseRef(t *testing.T, paths map[string]any, path, name string) {
	t.Helper()
	op := paths[path].(map[string]any)["get"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	want := "#/components/schemas/" + name
	if schema["$ref"] != want {
		t.Fatalf("%s response schema = %#v, want ref %q", path, schema, want)
	}
}

func TestAuditedListSchemasAreArrays(t *testing.T) {
	components := responseComponents()["schemas"].(map[string]any)
	for _, name := range []string{"WorkspaceList", "CrewList", "AgentList", "ProjectList", "IssueList", "SkillList"} {
		schema := components[name].(map[string]any)
		if schema["type"] != "array" {
			t.Errorf("%s type = %v, want array", name, schema["type"])
		}
	}
}
