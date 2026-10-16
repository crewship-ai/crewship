package main

import "testing"

func TestChatDecisionCatalogUsesFrozenSourceContracts(t *testing.T) {
	routes, components := chatDecisionSchemaCatalog()
	for _, key := range []string{"GET /api/v1/chats/{chatId}/decisions", "GET /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}", "POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer"} {
		if routes[key].Response == nil {
			t.Fatalf("missing typed response for %s", key)
		}
	}
	answer := routes["POST /api/v1/chats/{chatId}/decisions/{sourceType}/{sourceId}/answer"]
	if !answer.RequestRequired || len(answer.Parameters) != 1 || answer.Parameters[0]["name"] != "Idempotency-Key" || answer.Parameters[0]["required"] != true {
		t.Fatal("missing required operation key/body")
	}
	response := components["ChatDecisionReference"].(map[string]any)
	if response["properties"].(map[string]any)["decision"] == nil || response["properties"].(map[string]any)["proposal"] != nil {
		t.Fatal("decision route exposes proposal union")
	}
}
