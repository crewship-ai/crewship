package main

import "testing"

func TestCraftNativeAgentContractKeepsExplicitConsentSeparateFromHumanAdmission(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/pages/{slug}/craft/tasks"}, {method: "POST", path: "/api/v1/pages/{slug}/craft/tasks/cancel"}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	consent, ok := schemas["NativeCraftAgentTaskRequest"].(map[string]any)
	if !ok {
		t.Fatal("native agent consent has no concrete schema")
	}
	if consent["additionalProperties"] != false {
		t.Fatal("consent admits source/run authority selectors")
	}
	props := consent["properties"].(map[string]any)
	if len(props) != 3 || props["whole_document"].(map[string]any)["type"] != "boolean" {
		t.Fatal("explicit whole-document consent missing")
	}
	available := schemas["NativeCraftAvailability"].(map[string]any)["properties"].(map[string]any)
	if available["agents_enabled"] == nil {
		t.Fatal("default-off agent availability missing")
	}
	lease := schemas["NativeCraftLease"].(map[string]any)["properties"].(map[string]any)
	if lease["can_stop_agent"] == nil {
		t.Fatal("human stop capability undocumented")
	}
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/pages/{slug}/craft/tasks", "/api/v1/pages/{slug}/craft/tasks/cancel"} {
		op := paths[path].(map[string]any)["post"].(map[string]any)
		if params, ok := op["parameters"].([]map[string]any); ok {
			for _, param := range params {
				if param["name"] == "X-Craft-Session" {
					t.Fatal("human consent borrows editor admission")
				}
			}
		}
	}
}
