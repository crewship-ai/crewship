package main

import "testing"

func TestBoardInitializationPublishedContract(t *testing.T) {
	key := "POST /api/v1/pages/{slug}/board"
	schema, ok := routeSchemaCatalog()[key]
	if !ok || !schema.RequestRequired || schema.Request["additionalProperties"] != false {
		t.Fatalf("Board init requires a closed mutation request: %#v", schema)
	}
	properties := schema.Request["properties"].(map[string]any)
	if len(properties) != 1 || properties["mutation_id"].(map[string]any)["maxLength"] != 128 {
		t.Fatalf("Board init must retain its bounded stable receipt identity: %#v", properties)
	}
	for _, method := range []string{"GET", "POST"} {
		document := buildDocument([]route{routeFromKey(method + " /api/v1/pages/{slug}/board")})
		operation := document["paths"].(map[string]any)["/api/v1/pages/{slug}/board"].(map[string]any)
		verb := "get"
		if method == "POST" {
			verb = "post"
		}
		response := operation[verb].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		props, ok := response["properties"].(map[string]any)
		if !ok || len(props) != 8 || response["additionalProperties"] != false {
			t.Fatalf("untyped Board snapshot: %#v", response)
		}
		if props["seq"].(map[string]any)["maximum"] != 0 {
			t.Fatalf("reader must not imply support for future drawing state")
		}
		for _, name := range []string{"records", "placements", "placement_tombstones"} {
			if props[name].(map[string]any)["maxItems"] != 0 {
				t.Fatalf("%s must remain confirmed empty", name)
			}
		}
	}
}
