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

// Restore retries must retain the original immutable scope and CAS, rather than
// recapture the current head after an uncertain acknowledgement.
func TestDocumentRestoreRequiresOriginalPacketContract(t *testing.T) {
	c, ok := documentSchemaCatalog()["POST /api/v1/pages/{slug}/document/restore"]
	if !ok {
		t.Fatal("restore has no versioned wire contract")
	}
	if c.Response["$ref"] != "#/components/schemas/DocumentRestoreReceipt" || !c.RequestRequired {
		t.Fatal("restore receipt or required request missing")
	}
	props, ok := c.Request["properties"].(map[string]any)
	if !ok || len(props) != 6 || c.Request["additionalProperties"] != false {
		t.Fatal("restore packet must be closed")
	}
	required, ok := c.Request["required"].([]string)
	if !ok || len(required) != 6 {
		t.Fatal("all six original fields must be required")
	}
	for _, key := range []string{"page_id", "id", "schema", "epoch", "ifRev", "checkpoint"} {
		found := false
		for _, name := range required {
			if key == name {
				found = true
			}
		}
		if !found || props[key] == nil {
			t.Fatalf("missing original %s", key)
		}
	}
}
