package main

import "testing"

func TestPageDocumentStagingContract(t *testing.T) {
	catalog := pagesSchemaCatalog()
	for _, key := range []string{"GET /api/v1/pages/{slug}", "GET /api/v1/pages"} {
		schema := catalog[key].Response
		if key == "GET /api/v1/pages" {
			schema = schema["items"].(map[string]any)
		}
		properties := schema["properties"].(map[string]any)
		field, ok := properties["has_document"].(map[string]any)
		if !ok || field["type"] != "boolean" {
			t.Errorf("%s omits real document metadata", key)
		}
	}
	create := catalog["POST /api/v1/pages"].Request["properties"].(map[string]any)
	empty, ok := create["allow_empty"].(map[string]any)
	if !ok || empty["type"] != "boolean" {
		t.Error("explicit empty human staging is absent from Page create request")
	}
	patch := catalog["PATCH /api/v1/pages/{slug}"].Request["properties"].(map[string]any)
	if _, ok := patch["allow_empty"]; ok {
		t.Error("creation opt-in leaked into metadata update contract")
	}
}
