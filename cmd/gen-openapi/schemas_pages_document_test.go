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
		for name, typ := range map[string]string{"private_audience": "boolean", "has_slides": "boolean", "slides_schema": "integer"} {
			field, ok := properties[name].(map[string]any)
			if !ok || field["type"] != typ || field["readOnly"] != true {
				t.Errorf("%s lacks read-only %s", key, name)
			}
		}
	}
	create := catalog["POST /api/v1/pages"].Request["properties"].(map[string]any)
	empty, ok := create["allow_empty"].(map[string]any)
	if !ok || empty["type"] != "boolean" {
		t.Error("explicit empty human staging is absent from Page create request")
	}
	if _, ok := create["private"]; ok {
		t.Error("private body flag must not be accepted by legacy creation")
	}
	private, ok := catalog["POST /api/v1/private-pages"]
	if !ok || private.Request == nil || private.Response == nil {
		t.Error("separate private constructor is absent")
	}
	patch := catalog["PATCH /api/v1/pages/{slug}"].Request["properties"].(map[string]any)
	if _, ok := patch["allow_empty"]; ok {
		t.Error("creation opt-in leaked into metadata update contract")
	}
	if _, ok := patch["private"]; ok {
		t.Error("creation audience opt-in leaked into update")
	}
}
