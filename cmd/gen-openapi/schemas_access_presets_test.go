package main

import "testing"

func TestAccessPresetCatalogTypedContract(t *testing.T) {
	routes, schemas := accessPresetSchemaCatalog()
	route := routes["GET /api/v1/workspaces/{workspaceId}/access/presets"]
	if route.Response["$ref"] != "#/components/schemas/AccessPresetCatalog" {
		t.Fatal("catalog response must be typed")
	}
	catalog := schemas["AccessPresetCatalog"].(map[string]any)
	if catalog["additionalProperties"] != false || len(catalog["required"].([]string)) != 1 {
		t.Fatal("incomplete catalog envelope")
	}
	definition := schemas["AccessPresetDefinition"].(map[string]any)
	if len(definition["required"].([]string)) != 5 {
		t.Fatal("missing immutable definition fields")
	}
	entry := schemas["AccessPresetCatalogEntry"].(map[string]any)["properties"].(map[string]any)
	if entry["definition_hash"] == nil {
		t.Fatal("missing definition hash")
	}
}
