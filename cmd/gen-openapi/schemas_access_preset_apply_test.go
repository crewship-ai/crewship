package main

import "testing"

func TestAccessPresetApplyTypedContract(t *testing.T) {
	routes, schemas := accessPresetSchemaCatalog()
	route := routes["POST /api/v1/workspaces/{workspaceId}/members/{memberId}/access/presets/apply"]
	if !route.RequestRequired || route.Response["$ref"] != "#/components/schemas/MemberAccessPolicy" {
		t.Fatal("apply needs explicit consent and complete canonical policy response")
	}
	schema := schemas["AccessPresetApplyRequest"].(map[string]any)
	if schema["additionalProperties"] != false || len(schema["required"].([]string)) != 2 {
		t.Fatal("apply consent must reject extra actor/rights fields")
	}
	props := schema["properties"].(map[string]any)
	if props["selection"].(map[string]any)["$ref"] != "#/components/schemas/AccessPresetRequest" || props["confirmation_digest"].(map[string]any)["pattern"] != "^[A-Fa-f0-9]{64}$" {
		t.Fatal("apply consent lost snapshot or digest binding")
	}
}
