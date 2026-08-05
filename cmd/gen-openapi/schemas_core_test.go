package main

import "testing"

func TestCoreResourceSchemasCoverHandlerContracts(t *testing.T) {
	schemas := coreResourceSchemas()
	for _, name := range []string{
		"Workspace", "WorkspaceList", "Crew", "CrewList", "Agent", "AgentList", "Project", "ProjectList",
		"WorkspaceCreateRequest", "WorkspaceUpdateRequest", "CrewCreateRequest", "CrewUpdateRequest",
		"AgentCreateRequest", "AgentUpdateRequest", "ProjectCreateRequest", "ProjectUpdateRequest", "HireRequest", "HireResponse",
	} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("missing core schema %q", name)
		}
	}
}

func TestCoreResourceSchemasUseNullablePointersAndRequiredValues(t *testing.T) {
	schemas := coreResourceSchemas()
	workspace := schemas["Workspace"].(map[string]any)
	workspaceProps := workspace["properties"].(map[string]any)
	if workspaceProps["logo_url"].(map[string]any)["nullable"] != true {
		t.Error("Workspace.logo_url must be nullable")
	}
	if !containsString(workspace["required"].([]string), "id") {
		t.Error("Workspace.id must be required")
	}

	projectCreate := schemas["ProjectCreateRequest"].(map[string]any)
	if !containsString(projectCreate["required"].([]string), "name") {
		t.Error("ProjectCreateRequest.name must be required")
	}
	projectProps := projectCreate["properties"].(map[string]any)
	if projectProps["lead_type"].(map[string]any)["enum"].([]string)[0] != "user" {
		t.Error("ProjectCreateRequest.lead_type must constrain the handler enum")
	}
}

func TestCoreResourceSchemasSeparateCreateAndUpdateFields(t *testing.T) {
	schemas := coreResourceSchemas()
	create := schemas["CrewCreateRequest"].(map[string]any)["properties"].(map[string]any)
	update := schemas["CrewUpdateRequest"].(map[string]any)["properties"].(map[string]any)
	if _, ok := create["max_ephemeral_agents"]; ok {
		t.Error("CrewCreateRequest must not advertise update-only max_ephemeral_agents")
	}
	if _, ok := update["max_ephemeral_agents"]; !ok {
		t.Error("CrewUpdateRequest must advertise max_ephemeral_agents")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
