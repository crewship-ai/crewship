package main

import "testing"

func TestFinalIntegrationsConnectorsCatalogCoversGenericResponseSurface(t *testing.T) {
	routes, components := finalIntegrationsConnectorsSchemaCatalog()
	want := map[string]string{
		"GET /api/v1/integrations/composio/inventory":             "FinalComposioInventory",
		"GET /api/v1/integrations/composio/default":               "FinalComposioDefault",
		"GET /api/v1/integrations/composio/agents/{agentId}/bind": "FinalComposioBindings",
		"POST /api/v1/credentials/{credentialId}/reveal":          "FinalCredentialReveal",
		"GET /api/v1/notification-channels":                       "FinalNotificationChannels",
		"GET /api/v1/notification-templates":                      "FinalNotificationTemplates",
		"GET /api/v1/agents/{agentId}/chats":                      "FinalChatList",
		"POST /api/v1/chats/{chatId}/steer":                       "FinalChatSteer",
		"GET /api/v1/inbox":                                       "FinalInboxList",
		"POST /api/v1/inbox/bulk":                                 "FinalInboxBulk",
		"POST /api/v1/webhooks/{crewId}/{agentId}/trigger":        "FinalWebhookFire",
		"GET /api/v1/users/me/user-model":                         "FinalUserModel",
		"GET /api/v1/me/preferences":                              "FinalPreferences",
	}
	for route, component := range want {
		schema, ok := routes[route]
		if !ok || schema.Response["$ref"] != "#/components/schemas/"+component {
			t.Errorf("route %s = %#v, want ref %s", route, schema.Response, component)
		}
		if _, ok := components[component]; !ok {
			t.Errorf("component %s missing for %s", component, route)
		}
	}
}

func TestFinalIntegrationsConnectorsCatalogUsesHandlerFields(t *testing.T) {
	_, components := finalIntegrationsConnectorsSchemaCatalog()
	checks := map[string][]string{
		"FinalCredentialReveal": {"credential_id", "sensitivity", "value", "journal_entry_id"},
		"FinalComposioDefault":  {"enabled_flag", "default_user_id", "default_mcp_server_id", "connected_user_count"},
		"FinalInboxBulk":        {"updated", "skipped", "skipped_ids"},
		"FinalChatSteer":        {"queued", "in_flight"},
		"FinalUserModel":        {"user_id", "workspace_id", "exists", "facts"},
	}
	for component, fields := range checks {
		properties := components[component].(map[string]any)["properties"].(map[string]any)
		for _, field := range fields {
			if _, ok := properties[field]; !ok {
				t.Errorf("component %s missing handler field %s", component, field)
			}
		}
	}
}

func TestFinalIntegrationsConnectorsCatalogIsWiredIntoDocument(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/integrations/composio/default"}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["FinalComposioDefault"]; !ok {
		t.Fatal("final catalog component is not wired into OpenAPI components")
	}
	response := doc["paths"].(map[string]any)["/api/v1/integrations/composio/default"].(map[string]any)["get"].(map[string]any)
	got := response["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if got.(map[string]any)["$ref"] != "#/components/schemas/FinalComposioDefault" {
		t.Fatalf("response schema = %#v", got)
	}
}
