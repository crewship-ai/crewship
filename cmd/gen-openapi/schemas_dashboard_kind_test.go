package main

import "testing"

func TestDashboardKindMetadataAppearsInPageListAndDetailSchemas(t *testing.T) {
	routes := pagesSchemaCatalog()
	for _, key := range []string{"GET /api/v1/pages", "GET /api/v1/pages/{slug}"} {
		schema := routes[key].Response
		if key == "GET /api/v1/pages" {
			schema = schema["items"].(map[string]any)
		}
		props := schema["properties"].(map[string]any)
		kind := props["artifact_kind"].(map[string]any)
		found := false
		for _, v := range kind["enum"].([]string) {
			if v == "dashboard" {
				found = true
			}
		}
		if !found || props["artifact_shared_version"].(map[string]any)["minimum"] != 1 {
			t.Fatalf("missing authorized Dashboard metadata on %s", key)
		}
	}
}
