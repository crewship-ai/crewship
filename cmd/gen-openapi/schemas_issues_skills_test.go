package main

import "testing"

func TestDomainSchemaMapCoversDomainResourcesAndRequests(t *testing.T) {
	schemas := issueSkillCredentialSchemaComponents()
	for _, name := range []string{
		"Issue", "IssueList", "Label", "LabelList", "Skill", "SkillDetail", "SkillList",
		"Credential", "CredentialList", "CredentialPage", "CredentialField", "CredentialBinding", "AgentCredential", "AgentCredentialList",
	} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("missing schema %q", name)
		}
	}
}

func TestDomainSchemaMapIssueAndLabelShapes(t *testing.T) {
	schemas := issueSkillCredentialSchemaComponents()
	issue := schemas["Issue"].(map[string]any)
	props := issue["properties"].(map[string]any)
	for _, field := range []string{"identifier", "description", "assignee_id", "completed_at", "project_id", "routine_id", "created_by"} {
		if _, ok := props[field]; !ok {
			t.Errorf("Issue missing property %q", field)
		}
	}
	labels := props["labels"].(map[string]any)
	if labels["type"] != "array" || labels["items"].(map[string]any)["$ref"] != "#/components/schemas/Label" {
		t.Fatalf("Issue.labels = %#v, want array of Label", labels)
	}
	if got := schemas["Label"].(map[string]any)["properties"].(map[string]any)["label_group"].(map[string]any)["nullable"]; got != true {
		t.Fatalf("Label.label_group nullable = %v, want true", got)
	}
	_, requests := coreResourceRequestSchemaCatalogV2()
	assertRequired(t, requests["CoreIssueCreateRequestV2"], "title")
	assertRequired(t, requests["CoreLabelCreateRequestV2"], "name", "color")
}

func TestDomainSchemaMapCredentialNeverExposesSecretValue(t *testing.T) {
	schemas := issueSkillCredentialSchemaComponents()
	credential := schemas["Credential"].(map[string]any)["properties"].(map[string]any)
	if _, ok := credential["value"]; ok {
		t.Fatal("Credential response must not expose the secret value")
	}
	for _, field := range []string{"endpoint_url", "username", "last_used_ips", "tags", "security_level", "provisioned_for_service"} {
		if _, ok := credential[field]; !ok {
			t.Errorf("Credential missing metadata property %q", field)
		}
	}
	_, requests := coreResourceRequestSchemaCatalogV2()
	request := requests["CoreCredentialCreateRequestV2"].(map[string]any)["properties"].(map[string]any)
	if _, ok := request["value"]; !ok {
		t.Fatal("CoreCredentialCreateRequestV2 must describe value input")
	}
	if _, ok := request["mode"]; !ok {
		t.Fatal("CoreCredentialCreateRequestV2 must describe the PROVIDER_LOGIN mode input")
	}
}

func TestDomainSchemaMapSkillDetailAddsHandlerFields(t *testing.T) {
	schemas := issueSkillCredentialSchemaComponents()
	base := schemas["Skill"].(map[string]any)["properties"].(map[string]any)
	detail := schemas["SkillDetail"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"content", "credential_requirements", "mcp_server_command", "dependencies", "security_score", "allowed_domains", "changelog"} {
		if _, ok := detail[field]; !ok {
			t.Errorf("SkillDetail missing property %q", field)
		}
	}
	if _, ok := base["content"]; ok {
		t.Fatal("Skill list schema must not claim detail-only content")
	}
	// Every field the handler emits is emitted unconditionally, so the
	// detail-only fields are required too — the pair in
	// internal/api/openapi_response_shape_test.go derives this from the
	// struct tags; this only pins that the list is not the base one.
	assertRequired(t, schemas["SkillDetail"], "content", "agent_count", "changelog", "description")
}

func assertRequired(t *testing.T, raw any, fields ...string) {
	t.Helper()
	schema := raw.(map[string]any)
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("schema required = %#v, want []string", schema["required"])
	}
	for _, want := range fields {
		found := false
		for _, got := range required {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("required fields %v missing %q", required, want)
		}
	}
}
