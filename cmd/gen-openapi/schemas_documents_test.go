package main

import (
	"strings"
	"testing"
)

func TestHumanDocumentOperationsHaveVersionedWireContracts(t *testing.T) {
	for _, tc := range []struct{ method, path, component string }{
		{"GET", "/api/v1/documents/status", "DocumentAvailability"},
		{"GET", "/api/v1/pages/{slug}/document", "DocumentState"},
		{"POST", "/api/v1/pages/{slug}/document/patch", "DocumentReceipt"},
		{"POST", "/api/v1/pages/{slug}/document/initialize", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			document := buildDocument([]route{routeFromKey(tc.method + " " + tc.path)})
			operation := document["paths"].(map[string]any)[openAPIPath(tc.path)].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
			response := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if tc.component != "" {
				if response["$ref"] != "#/components/schemas/"+tc.component {
					t.Fatalf("missing versioned document wire contract: %#v", response)
				}
			} else {
				alternatives, ok := response["oneOf"].([]any)
				if !ok || len(alternatives) != 2 {
					t.Fatalf("initialization loses first-state/exact-retry distinction: %#v", response)
				}
				for i, name := range []string{"DocumentInitializationState", "DocumentInitializeReceipt"} {
					if alternatives[i].(map[string]any)["$ref"] != "#/components/schemas/"+name {
						t.Fatalf("unexpected initialization alternative: %#v", alternatives[i])
					}
				}
			}
		})
	}
}
