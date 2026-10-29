package main

import "testing"

func TestCraftNativeLeaseSchemaKeepsOpaqueGenerationAndPrivateHeader(t *testing.T) {
	path := "/api/v1/pages/{slug}/craft/lease"
	doc := buildDocument([]route{{method: "GET", path: path}, {method: "POST", path: "/api/v1/pages/{slug}/craft/lease/handoff"}})
	paths := doc["paths"].(map[string]any)
	op := paths[path].(map[string]any)["get"].(map[string]any)
	if responseSchemaFromOperation(t, op)["$ref"] != "#/components/schemas/NativeCraftLease" {
		t.Fatal("native lease has no concrete wire schema")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["NativeCraftLease"].(map[string]any)["properties"].(map[string]any)
	if props["generation"].(map[string]any)["type"] != "string" {
		t.Fatal("native generation must never be a JSON number")
	}
	if props["handoff_requested_by"].(map[string]any)["type"] != "array" {
		t.Fatal("handoff requests must remain an array")
	}
	parameters := op["parameters"].([]map[string]any)
	found := false
	for _, p := range parameters {
		if p["name"] == "X-Craft-Session" && p["in"] == "header" && p["required"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("private admission header missing from lease contract")
	}
}
