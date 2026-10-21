package main

import "testing"

func TestAccessPresetTypedContract(t *testing.T) {
	routes, schemas := accessPresetSchemaCatalog()
	base := "/api/v1/workspaces/{workspaceId}"
	catalog := routes["GET "+base+"/access/presets"]
	preview := routes["POST "+base+"/members/{memberId}/access/presets/preview"]
	if catalog.Response["$ref"] != "#/components/schemas/AccessPresetCatalog" || !preview.RequestRequired {
		t.Fatal("missing typed routes or mandatory preview request")
	}
	request := schemas["AccessPresetRequest"].(map[string]any)
	if request["additionalProperties"] != false {
		t.Fatal("unknown input must be rejected")
	}
	p := request["properties"].(map[string]any)
	if p["resource"].(map[string]any)["additionalProperties"] != false {
		t.Fatal("nested unknown input must be rejected")
	}
	response := schemas["AccessPresetPreview"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"before", "changes", "after_rights", "orthogonal_rights", "warnings"} {
		shape := response[key].(map[string]any)
		if shape["type"] != "array" || shape["nullable"] == true {
			t.Fatalf("%s must be a complete non-null array", key)
		}
	}
	if response["current_selection"].(map[string]any)["nullable"] != true {
		t.Fatal("unsaved selection must be nullable")
	}
	change := schemas["AccessPresetChange"].(map[string]any)
	for _, key := range change["required"].([]string) {
		if key == "created_at" {
			t.Fatal("preview must not promise future grant timestamps")
		}
	}
	grant := memberAccessSchemaComponents()["MemberAccessGrant"].(map[string]any)
	if len(grant["required"].([]string)) != 6 {
		t.Fatal("incomplete grant provenance")
	}
}
