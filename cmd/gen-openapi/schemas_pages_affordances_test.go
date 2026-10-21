package main

import "testing"

func TestPagesAffordanceSchemasDescribeActualCallerFields(t *testing.T) {
	catalog := pagesSchemaCatalog()
	folders := catalog["GET /api/v1/page-folders"].Response["properties"].(map[string]any)["folders"].(map[string]any)["items"].(map[string]any)
	for _, field := range []string{"can_rename", "can_share", "can_delete", "can_accept_pages"} {
		property, ok := folders["properties"].(map[string]any)[field].(map[string]any)
		if !ok || property["type"] != "boolean" {
			t.Fatalf("folder authority missing boolean %s", field)
		}
	}
	pages := catalog["GET /api/v1/pages"].Response["items"].(map[string]any)
	for _, field := range []string{"can_move", "can_remove_from_folder"} {
		property, ok := pages["properties"].(map[string]any)[field].(map[string]any)
		if !ok || property["type"] != "boolean" {
			t.Fatalf("page authority missing boolean %s", field)
		}
	}
}
