package main

import "testing"

func TestPageIndexPrivateAudienceReadOnlyContract(t *testing.T) {
	catalog := pagesSchemaCatalog()
	row := catalog["GET /api/v1/pages"].Response["items"].(map[string]any)
	properties := row["properties"].(map[string]any)
	field, ok := properties["private_audience"].(map[string]any)
	if !ok || field["type"] != "boolean" || field["readOnly"] != true {
		t.Fatal("authorized Page index omits explicit read-only audience classification")
	}
	for _, route := range []string{"POST /api/v1/pages", "PATCH /api/v1/pages/{slug}"} {
		properties := catalog[route].Request["properties"].(map[string]any)
		if _, exists := properties["private_audience"]; exists {
			t.Errorf("%s must not grant a client permission to change audience", route)
		}
	}
}
