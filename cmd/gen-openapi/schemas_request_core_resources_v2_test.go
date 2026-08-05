package main

import "testing"

func TestCoreResourceRequestSchemaCatalogV2IsWiredAndPrecise(t *testing.T) {
	routes, components := coreResourceRequestSchemaCatalogV2()
	checks := map[string]string{
		"POST /api/v1/workspaces":                             "CoreWorkspaceCreateRequestV2",
		"PATCH /api/v1/crews/{crewId}":                        "CoreCrewUpdateRequestV2",
		"POST /api/v1/agents":                                 "CoreAgentCreateRequestV2",
		"POST /api/v1/crews/{crewId}/issues":                  "CoreIssueCreateRequestV2",
		"POST /api/v1/workspaces/{workspaceId}/skills/import": "CoreSkillImportRequestV2",
		"PATCH /api/v1/credentials/{credentialId}":            "CoreCredentialUpdateRequestV2",
	}
	for route, name := range checks {
		got, ok := routes[route]
		if !ok || got.Request["$ref"] != "#/components/schemas/"+name {
			t.Fatalf("%s is not wired to %s: %#v", route, name, got)
		}
		if _, ok := components[name]; !ok {
			t.Fatalf("missing component %s", name)
		}
	}
}

func TestCoreResourceRequestSchemaCatalogV2FieldsRequiredNullableAndEnums(t *testing.T) {
	components := func() map[string]any { _, c := coreResourceRequestSchemaCatalogV2(); return c }()
	workspace := components["CoreWorkspaceCreateRequestV2"].(map[string]any)
	if got := workspace["required"].([]string); len(got) != 2 || got[0] != "name" || got[1] != "slug" {
		t.Fatalf("workspace required = %#v", got)
	}
	issue := components["CoreIssueUpdateRequestV2"].(map[string]any)["properties"].(map[string]any)
	if issue["routine_inputs"].(map[string]any)["nullable"] != true {
		t.Fatal("issue routine_inputs must be nullable")
	}
	agent := components["CoreAgentCreateRequestV2"].(map[string]any)["properties"].(map[string]any)
	roles := agent["agent_role"].(map[string]any)["enum"].([]string)
	if len(roles) != 2 || roles[0] != "AGENT" || roles[1] != "LEAD" {
		t.Fatalf("agent_role enum = %#v", roles)
	}
	credential := components["CoreCredentialUpdateRequestV2"].(map[string]any)["properties"].(map[string]any)
	if _, ok := credential["value"]; ok {
		t.Fatal("credential update must not expose immutable value")
	}
}

func TestCoreResourceRequestSchemaCatalogV2ReplacesGenericBodies(t *testing.T) {
	doc := buildDocument([]route{
		{method: "POST", path: "/api/v1/crews/{crewId}/issues"},
		{method: "PATCH", path: "/api/v1/credentials/{credentialId}"},
	})
	paths := doc["paths"].(map[string]any)
	for path, method := range map[string]string{"/api/v1/crews/{crewId}/issues": "post", "/api/v1/credentials/{credentialId}": "patch"} {
		op := paths[path].(map[string]any)[method].(map[string]any)
		schema := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if schema["$ref"] == nil {
			t.Fatalf("%s request body is not a concrete component ref: %#v", path, schema)
		}
	}
}
