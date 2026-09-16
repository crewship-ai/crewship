package main

import "testing"

func TestCoreResourceSchemasCoverHandlerContracts(t *testing.T) {
	schemas := coreResourceSchemas()
	for _, name := range []string{
		"Workspace", "WorkspaceList", "Crew", "CrewList", "Agent", "AgentList", "Project", "ProjectList",
		"HireRequest", "HireResponse",
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

	_, requests := coreResourceRequestSchemaCatalogV2()
	projectCreate := requests["CoreProjectCreateRequestV2"].(map[string]any)
	if !containsString(projectCreate["required"].([]string), "name") {
		t.Error("CoreProjectCreateRequestV2.name must be required")
	}
	projectProps := projectCreate["properties"].(map[string]any)
	if projectProps["lead_type"].(map[string]any)["enum"].([]string)[0] != "user" {
		t.Error("CoreProjectCreateRequestV2.lead_type must constrain the handler enum")
	}
}

func TestCoreResourceSchemasSeparateCreateAndUpdateFields(t *testing.T) {
	_, schemas := coreResourceRequestSchemaCatalogV2()
	create := schemas["CoreCrewCreateRequestV2"].(map[string]any)["properties"].(map[string]any)
	update := schemas["CoreCrewUpdateRequestV2"].(map[string]any)["properties"].(map[string]any)
	if _, ok := create["max_ephemeral_agents"]; ok {
		t.Error("CoreCrewCreateRequestV2 must not advertise update-only max_ephemeral_agents")
	}
	if _, ok := update["max_ephemeral_agents"]; !ok {
		t.Error("CoreCrewUpdateRequestV2 must advertise max_ephemeral_agents")
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
