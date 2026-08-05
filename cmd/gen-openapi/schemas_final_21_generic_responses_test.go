package main

import (
	"strings"
	"testing"
)

func TestFinal21GenericResponseSchemaCatalogCoversAllContracts(t *testing.T) {
	catalog := final21GenericResponseSchemaCatalog()
	want := []string{
		"GET /api/v1/admin/backups/inspect", "POST /api/v1/admin/keeper/ask",
		"GET /api/v1/admin/log-level", "PUT /api/v1/admin/log-level",
		"DELETE /api/v1/chats/{chatId}/participants/{userId}", "DELETE /api/v1/checkpoints/{id}",
		"DELETE /api/v1/crew-connections/{connectionId}", "DELETE /api/v1/instance/settings/{key}",
		"DELETE /api/v1/me/preferences/{key}", "PUT /api/v1/me/preferences/{key}", "DELETE /api/v1/milestones/{milestoneId}",
		"DELETE /api/v1/projects/{projectId}", "DELETE /api/v1/recurring-issues/{recurringId}",
		"DELETE /api/v1/triage-rules/{ruleId}", "DELETE /api/v1/notification-templates",
		"DELETE /api/v1/notification-channels/{id}",
		"POST /api/v1/integrations/composio/accounts/{accountId}/revoke",
		"POST /api/v1/integrations/composio/accounts/{accountId}/refresh",
		"DELETE /api/v1/integrations/composio/accounts/{accountId}",
		"DELETE /api/v1/integrations/composio/agents/{agentId}/bind",
		"DELETE /api/v1/notification-channels/{id}/agents/{agentId}",
		"GET /api/v1/oauth/providers", "GET /api/v1/slash-commands",
		"POST /api/v1/waitpoint-tokens/{token}",
	}
	if len(catalog) != len(want) {
		t.Fatalf("catalog has %d contracts, want %d", len(catalog), len(want))
	}
	for _, route := range want {
		if _, ok := catalog[route]; !ok {
			t.Errorf("catalog missing %q", route)
		}
	}
}

func TestFinal21GenericNoContentContractsEmitOnly204(t *testing.T) {
	for routeKey, contract := range final21GenericResponseSchemaCatalog() {
		if contract.SuccessStatuses == nil || len(contract.SuccessStatuses) != 1 || contract.SuccessStatuses[0] != "204" {
			continue
		}
		rt := routeFromKey(routeKey)
		doc := buildDocument([]route{rt})
		op := doc["paths"].(map[string]any)[openAPIPath(rt.path)].(map[string]any)[stringLower(rt.method)]
		responses := op.(map[string]any)["responses"].(map[string]any)
		if len(responses) != 1 {
			t.Errorf("%s responses = %#v, want only 204", routeKey, responses)
		}
		if _, ok := responses["204"]; !ok {
			t.Errorf("%s has no 204 response: %#v", routeKey, responses)
		}
	}
}

func TestFinal21GenericJSONContractsHaveContent(t *testing.T) {
	for _, routeKey := range []string{
		"GET /api/v1/admin/backups/inspect", "POST /api/v1/admin/keeper/ask",
		"GET /api/v1/admin/log-level", "PUT /api/v1/admin/log-level",
		"DELETE /api/v1/notification-channels/{id}", "GET /api/v1/oauth/providers",
		"GET /api/v1/slash-commands", "POST /api/v1/waitpoint-tokens/{token}",
	} {
		rt := routeFromKey(routeKey)
		doc := buildDocument([]route{rt})
		op := doc["paths"].(map[string]any)[openAPIPath(rt.path)].(map[string]any)[stringLower(rt.method)].(map[string]any)
		if _, ok := op["responses"].(map[string]any)["200"].(map[string]any)["content"]; !ok {
			t.Errorf("%s has no JSON response content", routeKey)
		}
	}
}

func stringLower(s string) string {
	return strings.ToLower(s)
}

func routeFromKey(key string) route {
	parts := splitRouteKey(key)
	return route{method: parts[0], path: parts[1]}
}

func splitRouteKey(key string) []string {
	for i := 0; i < len(key); i++ {
		if key[i] == ' ' {
			return []string{key[:i], key[i+1:]}
		}
	}
	return []string{key, ""}
}
