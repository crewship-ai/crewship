package main

import (
	"encoding/json"
	"testing"
)

func TestArtifactDataSchemasKeepTransportClosedAndValuesOpen(t *testing.T) {
	raw, err := json.Marshal(buildDocument(nil))
	if err != nil {
		t.Fatal(err)
	}
	schema := compileAt(t, raw, "/components/schemas/ArtifactDataBatch")
	valid := map[string]any{"base_version": 1, "documents": []any{map[string]any{"id": "constructor", "ifVersion": 0, "value": map[string]any{"DisplayName": "North", "optional": nil, "prototype": "owned"}}}}
	if err := schema.Validate(valid); err != nil {
		t.Fatal(err)
	}
	valid["BASE_VERSION"] = 2
	if schema.Validate(valid) == nil {
		t.Fatal("case alias accepted")
	}
	routes, _ := artifactDashboardDataSchemaCatalog()
	for _, key := range []string{"GET /api/v1/pages/{slug}/artifact/data", "GET /api/v1/pages/{slug}/artifact/data/{collection}", "POST /api/v1/pages/{slug}/artifact/data/{collection}"} {
		if routes[key].Response == nil {
			t.Fatalf("missing route %s", key)
		}
	}
}
