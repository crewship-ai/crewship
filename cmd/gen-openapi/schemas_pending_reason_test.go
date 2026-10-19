package main

import "testing"

func TestPendingReceiptReasonCodeRequired(t *testing.T) {
	_, schemas := crewWorkspaceGETSchemaCatalogV1()
	for _, name := range []string{"WorkspacePendingRunsResponseV1", "WorkspacePendingRunResponseV1"} {
		schema := schemas[name].(map[string]any)
		if name == "WorkspacePendingRunsResponseV1" {
			schema = schema["items"].(map[string]any)
		}
		prop, exists := schema["properties"].(map[string]any)["reason_code"]
		if !exists || prop.(map[string]any)["type"] != "string" {
			t.Fatalf("%s missing reason_code string", name)
		}
		required := false
		for _, key := range schema["required"].([]string) {
			if key == "reason_code" {
				required = true
			}
		}
		if !required {
			t.Fatalf("%s reason_code must reject missing wire field", name)
		}
	}
}
