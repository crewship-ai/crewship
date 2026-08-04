package main

import "testing"

// auditedResponseContracts is intentionally maintained separately from
// auditedResponseName. If a handler's known response is removed from the
// generator, this table makes the regression fail instead of silently
// accepting the generic object fallback.
var auditedResponseContracts = []struct {
	path          string
	componentName string
}{
	{path: "/api/v1/workspaces", componentName: "WorkspaceList"},
	{path: "/api/v1/workspaces/{workspaceId}", componentName: "Workspace"},
	{path: "/api/v1/crews", componentName: "CrewList"},
	{path: "/api/v1/crews/{crewId}", componentName: "Crew"},
	{path: "/api/v1/agents", componentName: "AgentList"},
	{path: "/api/v1/agents/{agentId}", componentName: "Agent"},
	{path: "/api/v1/projects", componentName: "ProjectList"},
	{path: "/api/v1/projects/{projectId}", componentName: "ProjectListItem"},
	{path: "/api/v1/issues", componentName: "IssueList"},
	{path: "/api/v1/issues/{identifier}", componentName: "Issue"},
	{path: "/api/v1/skills", componentName: "SkillList"},
	{path: "/api/v1/skills/{skillId}", componentName: "Skill"},
	{path: "/api/v1/runs", componentName: "RunList"},
	{path: "/api/v1/runs/{id}", componentName: "Run"},
}

func TestAuditedResponseContractsNeverUseGenericObjectFallback(t *testing.T) {
	routes := make([]route, 0, len(auditedResponseContracts))
	for _, contract := range auditedResponseContracts {
		routes = append(routes, route{method: "GET", path: contract.path})
	}

	doc := buildDocument(routes)
	paths := doc["paths"].(map[string]any)
	components := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, contract := range auditedResponseContracts {
		contract := contract
		t.Run(contract.path, func(t *testing.T) {
			op := paths[contract.path].(map[string]any)["get"].(map[string]any)
			schema := responseSchemaFromOperation(t, op)
			wantRef := "#/components/schemas/" + contract.componentName
			if schema["$ref"] != wantRef {
				t.Fatalf("response schema = %#v, want concrete ref %q", schema, wantRef)
			}
			if _, ok := schema["type"]; ok {
				t.Fatalf("response schema = %#v, must remain a component reference", schema)
			}
			if _, ok := components[contract.componentName]; !ok {
				t.Fatalf("response ref %q has no matching component", wantRef)
			}
		})
	}
}

func TestAuditedResponseComponentsHaveConcreteShapes(t *testing.T) {
	schemas := responseComponents()["schemas"].(map[string]any)
	for _, contract := range auditedResponseContracts {
		contract := contract
		t.Run(contract.path, func(t *testing.T) {
			schema, ok := schemas[contract.componentName].(map[string]any)
			if !ok {
				t.Fatalf("component %q is missing or malformed", contract.componentName)
			}
			if schema["type"] == "object" && len(schema) == 1 {
				t.Fatalf("component %q is only a generic object", contract.componentName)
			}
			if _, ok := schema["$ref"]; ok {
				t.Fatalf("component %q unexpectedly aliases another schema: %#v", contract.componentName, schema)
			}
		})
	}
}

func responseSchemaFromOperation(t *testing.T, operation map[string]any) map[string]any {
	t.Helper()
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatal("operation has no responses")
	}
	response, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatal("operation has no 200 response")
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		t.Fatal("200 response has no content")
	}
	mediaType, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatal("200 response has no application/json content")
	}
	schema, ok := mediaType["schema"].(map[string]any)
	if !ok {
		t.Fatal("application/json response has no schema")
	}
	return schema
}

func TestAuditedResponseContractTableMatchesGenerator(t *testing.T) {
	for _, contract := range auditedResponseContracts {
		if got := auditedResponseName(contract.path); got != contract.componentName {
			t.Errorf("auditedResponseName(%q) = %q, want %q", contract.path, got, contract.componentName)
		}
	}
	if got := len(auditedResponseContracts); got != 14 {
		t.Fatalf("audited response contract count = %d, want 14", got)
	}
}
