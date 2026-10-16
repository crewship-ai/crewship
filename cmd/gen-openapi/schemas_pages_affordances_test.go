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

func TestPagesContentReferencesDescribeDerivedMetadata(t *testing.T) {
	catalog := pagesSchemaCatalog()
	detail := catalog["GET /api/v1/pages/{slug}"].Response
	list := catalog["GET /api/v1/pages"].Response["items"].(map[string]any)
	for _, schema := range []map[string]any{detail, list} {
		props := schema["properties"].(map[string]any)
		for _, field := range []string{"content_descriptor", "application_descriptor"} {
			reference, ok := props[field].(map[string]any)
			if !ok {
				t.Fatalf("missing %s", field)
			}
			fields := reference["properties"].(map[string]any)
			if fields["supported"].(map[string]any)["type"] != "boolean" || fields["schema_version"].(map[string]any)["type"] != "integer" {
				t.Fatalf("invalid reference %#v", reference)
			}
			// Type is intentionally open: future server formats must survive metadata
			// transport and be rejected at compiled client dispatch, not disappear.
			if _, closed := fields["type_id"].(map[string]any)["enum"]; closed {
				t.Fatal("future type hidden by enum")
			}
		}
	}
}
