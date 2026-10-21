package main

import "testing"

func TestChatEntityHelperErrorsAreDocumented(t *testing.T) {
	routes := []route{
		{method: "GET", path: "/api/v1/chats/{chatId}/entity-refs"},
		{method: "POST", path: "/api/v1/crews/{crewId}/issues/{identifier}/comments"},
	}
	paths := buildDocument(routes)["paths"].(map[string]any)
	refs := paths[routes[0].path].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)
	for _, status := range []string{"400", "404", "503"} {
		if refs[status] == nil {
			t.Fatalf("reference response %s is undocumented", status)
		}
	}
	comments := paths[routes[1].path].(map[string]any)["post"].(map[string]any)["responses"].(map[string]any)
	conflict, ok := comments["409"].(map[string]any)
	if !ok {
		t.Fatal("helper-produced comment idempotency conflict is undocumented")
	}
	schema := conflict["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	variants, ok := schema["anyOf"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatal("legacy Problem Details and operation errors must both be documented")
	}
	if variants[0].(map[string]any)["properties"].(map[string]any)["revision"] == nil {
		t.Fatal("revision conflict drops the current revision from the client contract")
	}
}
