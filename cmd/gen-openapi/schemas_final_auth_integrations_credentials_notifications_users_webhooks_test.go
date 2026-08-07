package main

import "testing"

func TestFinalAuthSurfaceCatalogCoversAllBodies(t *testing.T) {
	routes, components := finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog()
	if len(routes) != 17 {
		t.Fatalf("route count = %d, want 17", len(routes))
	}
	for route, contract := range routes {
		if contract.Request == nil || contract.Request["$ref"] == nil {
			t.Errorf("%s has no named request schema: %#v", route, contract.Request)
		}
	}
	for name, schema := range components {
		if schema == nil {
			t.Errorf("component %q is nil", name)
		}
	}
}

func TestFinalAuthSurfaceCatalogPinsDecodedFields(t *testing.T) {
	_, components := finalAuthIntegrationsCredentialsNotificationsUsersWebhooksSchemaCatalog()
	checks := map[string][]string{
		"FinalAuthSignupRequest":         {"full_name", "email", "password"},
		"FinalAuthResetRequest":          {"token", "new_password"},
		"FinalAuthCredentialTestRequest": {"value"},
		"FinalAuthRevealRequest":         {"reason"},
		"FinalAuthPeerConsentRequest":    {"opted_out"},
	}
	for name, fields := range checks {
		schema := components[name].(map[string]any)
		properties := schema["properties"].(map[string]any)
		required := map[string]bool{}
		if fields, ok := schema["required"].([]string); ok {
			for _, field := range fields {
				required[field] = true
			}
		}
		for _, field := range fields {
			if properties[field] == nil {
				t.Errorf("%s omits decoded field %q", name, field)
			}
			if name != "FinalAuthCredentialTestRequest" || field == "value" {
				if !required[field] {
					t.Errorf("%s does not require validated field %q", name, field)
				}
			}
		}
	}
	tools := components["FinalAuthRefreshToolsRequest"].(map[string]any)["properties"].(map[string]any)
	if tools["tools"] == nil {
		t.Fatal("FinalAuthRefreshToolsRequest omits optional tools")
	}
}

func TestFinalAuthSurfaceCatalogMergesWithoutReplacingResponses(t *testing.T) {
	doc := buildDocument([]route{
		{method: "POST", path: "/api/v1/integrations/{integrationId}/test"},
		{method: "POST", path: "/api/v1/webhooks/{token}"},
	})
	paths := doc["paths"].(map[string]any)
	op := paths["/api/v1/integrations/{integrationId}/test"].(map[string]any)["post"].(map[string]any)
	body := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if body["$ref"] != "#/components/schemas/FinalAuthEmptyRequest" {
		t.Fatalf("integration test request = %#v", body)
	}
	response := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/CredentialTestResponse" {
		t.Fatalf("integration test response was replaced: %#v", response)
	}
	webhook := paths["/api/v1/webhooks/{token}"].(map[string]any)["post"].(map[string]any)
	payload := webhook["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if payload["$ref"] != "#/components/schemas/FinalAuthWebhookPayload" {
		t.Fatalf("webhook request = %#v", payload)
	}
}
