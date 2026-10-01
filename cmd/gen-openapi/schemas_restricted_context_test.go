package main

import "testing"

func TestRestrictedGeneratedMediaAndBoundedSelection(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/chats/{chatId}/restricted-run", auth: true}, {method: "GET", path: "/api/v1/chats/{chatId}/restricted-files/{fileId}/download", auth: true}})
	paths := doc["paths"].(map[string]any)
	post := paths["/api/v1/chats/{chatId}/restricted-run"].(map[string]any)["post"].(map[string]any)
	success := post["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := success["text/event-stream"]; !ok {
		t.Fatalf("generated native execution lost streaming media: %#v", success)
	}
	get := paths["/api/v1/chats/{chatId}/restricted-files/{fileId}/download"].(map[string]any)["get"].(map[string]any)
	content := get["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := content["application/octet-stream"]; !ok {
		t.Fatalf("private download became JSON: %#v", content)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	req := schemas["RestrictedRunRequest"].(map[string]any)
	versions := req["properties"].(map[string]any)["project_file_versions"].(map[string]any)
	if versions["maxItems"] != 16 || versions["uniqueItems"] != true || req["additionalProperties"] != false {
		t.Fatalf("unbounded source selection or public authority injection: %#v", req)
	}
	outcome := schemas["RestrictedAttemptOutcome"].(map[string]any)["properties"].(map[string]any)
	for _, forbidden := range []string{"handle", "credential_id", "input", "output", "error"} {
		if _, ok := outcome[forbidden]; ok {
			t.Fatalf("content-bearing outcome contract: %s", forbidden)
		}
	}
}
