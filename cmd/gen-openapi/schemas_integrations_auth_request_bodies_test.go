package main

import "testing"

func TestIntegrationsAuthRequestBodyCatalogCoversAuditedRoutes(t *testing.T) {
	routes, components := integrationsAuthRequestBodySchemaCatalog()
	want := map[string]string{
		"POST /api/v1/integrations":                                "IntegrationsAuthWorkspaceCreateRequest",
		"POST /api/v1/crews/{crewId}/integrations":                 "IntegrationsAuthCrewCreateRequest",
		"POST /api/v1/integrations/composio/triggers":              "IntegrationsAuthComposioTriggerRequest",
		"POST /api/v1/integrations/composio/connect":               "IntegrationsAuthComposioConnectRequest",
		"POST /api/v1/integrations/composio/agents/{agentId}/bind": "IntegrationsAuthComposioBindRequest",
		"PUT /api/v1/integrations/composio/settings":               "IntegrationsAuthComposioSettingsRequest",
		"POST /api/v1/oauth/initiate":                              "IntegrationsAuthOAuthInitiateRequest",
		"POST /api/v1/oauth/exchange":                              "IntegrationsAuthOAuthExchangeRequest",
		"POST /api/v1/oauth/auto-connect":                          "IntegrationsAuthOAuthAutoConnectRequest",
		"POST /api/v1/notification-channels":                       "IntegrationsAuthNotificationChannelCreateRequest",
		"PUT /api/v1/notification-templates":                       "IntegrationsAuthNotificationTemplateRequest",
		"PUT /api/v1/me/notification-prefs":                        "IntegrationsAuthNotificationPreferencesRequest",
		"POST /api/v1/auth/cli-token":                              "IntegrationsAuthCLITokenRequest",
		"POST /api/v1/workspaces/{workspaceId}/pipeline-webhooks":  "PipelineWebhookCreateRequest",
	}
	for route, component := range want {
		contract, ok := routes[route]
		if !ok || contract.Request == nil {
			t.Fatalf("%s has no request contract", route)
		}
		if got := contract.Request["$ref"]; got != "#/components/schemas/"+component {
			t.Fatalf("%s request ref = %v, want %s", route, got, component)
		}
		if _, ok := components[component]; !ok {
			t.Fatalf("%s references missing component %q", route, component)
		}
	}
}

func TestIntegrationsAuthRequestBodyCatalogPinsHandlerRequiredFields(t *testing.T) {
	_, components := integrationsAuthRequestBodySchemaCatalog()
	checks := map[string][]string{
		"IntegrationsAuthComposioTriggerRequest":       {"slug", "user_id"},
		"IntegrationsAuthComposioConnectRequest":       {"toolkit", "user_id"},
		"IntegrationsAuthComposioBindRequest":          {"user_id"},
		"IntegrationsAuthOAuthExchangeRequest":         {"credential_id", "code"},
		"IntegrationsAuthNotificationPairAgentRequest": {"agent_id"},
		"IntegrationsAuthNotificationTemplateRequest":  {"category", "channel_id", "title", "body"},
	}
	for name, fields := range checks {
		schema := components[name].(map[string]any)
		got := map[string]bool{}
		for _, field := range schema["required"].([]string) {
			got[field] = true
		}
		for _, field := range fields {
			if !got[field] {
				t.Errorf("%s does not require %q", name, field)
			}
		}
	}
}

func TestIntegrationsAuthRequestBodyCatalogIsWiredIntoDocumentBuilder(t *testing.T) {
	doc := buildDocument([]route{
		{method: "POST", path: "/api/v1/auth/cli-token"},
		{method: "POST", path: "/api/v1/oauth/discover"},
		{method: "POST", path: "/api/v1/integrations/composio/triggers"},
		{method: "PUT", path: "/api/v1/me/notification-prefs"},
	})
	paths := doc["paths"].(map[string]any)
	for path, method := range map[string]string{
		"/api/v1/auth/cli-token":        "post",
		"/api/v1/oauth/discover":        "post",
		"/api/v1/me/notification-prefs": "put",
	} {
		op := paths[path].(map[string]any)[method].(map[string]any)
		body := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if body["$ref"] == nil {
			t.Errorf("%s %s retained an inline/generic request body: %#v", method, path, body)
		}
	}
	trigger := paths["/api/v1/integrations/composio/triggers"].(map[string]any)["post"].(map[string]any)
	response := trigger["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if response.(map[string]any)["$ref"] != "#/components/schemas/FinalComposioTrigger" {
		t.Fatalf("request catalog replaced the existing Composio response: %#v", response)
	}
}
