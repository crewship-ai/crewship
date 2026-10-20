package main

import (
	"encoding/json"
	"testing"
)

func TestChatArtifactPreviewContractIsReadOnlyAndClosed(t *testing.T) {
	const path = "/api/v1/chats/{chatId}/artifact-context/preview"
	doc := buildDocument([]route{{method: "GET", path: path, auth: true}})
	operation := doc["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
	params := operation["parameters"].([]map[string]any)
	found := false
	for _, p := range params {
		if p["name"] == "page_id" && p["in"] == "query" {
			found = p["required"] == true && p["schema"].(map[string]any)["pattern"] == "^[A-Za-z0-9_-]{1,128}$"
		}
	}
	if !found {
		t.Fatal("missing required immutable page_id query")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	schema := compileAt(t, raw, "/paths/"+esc(path)+"/get/responses/200/content/"+esc("application/json")+"/schema")
	good := `{"chat":{"id":"chat","visibility":"private","audience":"author_and_workspace_operators"},"target":{"page_id":"page","type":"document","audience":"author_and_workspace_owners","expected":{"schema":3,"rev":0,"generation":1}},"can_update":false,"reason":"targeted_iteration_unavailable"}`
	var value map[string]any
	if err := json.Unmarshal([]byte(good), &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("valid read-only preview: %v", err)
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["can_update"] = true },
		func(v map[string]any) { v["target"].(map[string]any)["title"] = "private title" },
		func(v map[string]any) {
			v["target"].(map[string]any)["expected"].(map[string]any)["schema"] = float64(2)
		},
		func(v map[string]any) {
			v["target"].(map[string]any)["expected"].(map[string]any)["generation"] = float64(-1)
		},
		func(v map[string]any) { v["chat"].(map[string]any)["visibility"] = "shared" },
		func(v map[string]any) { delete(v, "reason") },
	} {
		var invalid map[string]any
		if err := json.Unmarshal([]byte(good), &invalid); err != nil {
			t.Fatal(err)
		}
		mutate(invalid)
		if schema.Validate(invalid) == nil {
			t.Fatalf("unsafe or incomplete preview accepted: %#v", invalid)
		}
	}
	responses := operation["responses"].(map[string]any)
	for _, status := range []string{"400", "401", "403", "404", "500", "503"} {
		if responses[status] == nil {
			t.Errorf("missing response %s", status)
		}
	}
}
