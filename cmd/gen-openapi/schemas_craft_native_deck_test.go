package main

import (
	"reflect"
	"testing"
)

func TestCraftNativeDeckSchemasKeepClosedApplicationsAndRequiredStatus(t *testing.T) {
	doc := buildDocument([]route{{method: "POST", path: "/api/v1/pages/{slug}/craft/initialize"}, {method: "GET", path: "/api/v1/documents/status"}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, tc := range []struct{ name, field string }{
		{"NativeCraftSession", "application_id"}, {"NativeCraftSnapshotRequest", "applicationId"},
		{"NativeCraftInitializeRequest", "application_id"}, {"NativeCraftInitializeReceipt", "application_id"},
	} {
		props := schemas[tc.name].(map[string]any)["properties"].(map[string]any)
		if !reflect.DeepEqual(props[tc.field].(map[string]any)["enum"], []string{"wordcraft", "gridcraft", "deckcraft"}) {
			t.Fatalf("%s application contract is not the closed qualified set", tc.name)
		}
	}
	status := schemas["DocumentAvailability"].(map[string]any)
	if status["properties"].(map[string]any)["deckcraft_native"].(map[string]any)["type"] != "boolean" {
		t.Fatal("Deck status is not a boolean")
	}
	found := false
	for _, key := range status["required"].([]string) {
		if key == "deckcraft_native" {
			found = true
		}
	}
	if !found {
		t.Fatal("Deck default-off status can disappear from the wire contract")
	}
}
