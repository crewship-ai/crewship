package main

import "testing"

func TestSlidesHumanRoutesDescribeRetainedStateAndReceipt(t *testing.T) {
	for _, tc := range []struct{ route, component string }{
		{"GET /api/v1/pages/{slug}/slides", "NativeSlidesState"},
		{"GET /api/v1/pages/{slug}/slides/revisions/{revision}", "NativeSlidesState"},
		{"POST /api/v1/pages/{slug}/slides/initialize", "NativeSlidesReceipt"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			contract, ok := routeSchemaCatalog()[tc.route]
			if !ok || contract.Response["$ref"] != "#/components/schemas/"+tc.component {
				t.Fatalf("retained Slides wire contract missing: %+v", contract)
			}
			if tc.component == "NativeSlidesReceipt" {
				fields, ok := contract.Request["properties"].(map[string]any)
				if !ok || len(fields) != 5 || !contract.RequestRequired || contract.Request["additionalProperties"] != false {
					t.Fatal("initialization must describe the closed original request")
				}
			}
		})
	}
}
