package main

import "testing"

func TestPresetSelectionSchemaAdministrativeOnly(t *testing.T) {
	policy := memberAccessPolicySchema()["properties"].(map[string]any)
	selection := policy["preset_selections"].(map[string]any)
	if selection["type"] != "array" || selection["readOnly"] != true || selection["nullable"] == true || selection["items"].(map[string]any)["$ref"] != "#/components/schemas/AccessPresetSelection" {
		t.Fatal("incomplete informational selection contract")
	}
	own := memberEffectiveAccessSchema()["properties"].(map[string]any)
	if _, ok := own["preset_selections"]; ok {
		t.Fatal("own policy schema leaks administrative selection")
	}
	shape := presetSelectionSchemaComponents()["AccessPresetSelection"].(map[string]any)
	for _, field := range shape["required"].([]string) {
		if field == "selected_by" {
			t.Fatal("deleted historical selecting actor must be optional")
		}
	}
	properties := shape["properties"].(map[string]any)
	if properties["preset_id"].(map[string]any)["enum"] != nil || properties["definition_hash"].(map[string]any)["enum"] != nil {
		t.Fatal("historical Custom definitions must remain representable")
	}
}
