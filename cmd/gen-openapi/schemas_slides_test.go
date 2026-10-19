package main

import "testing"

func TestSlidesHumanReadHasNamedSchemaOneContract(t *testing.T) {
	document := buildDocument([]route{routeFromKey("GET /api/v1/pages/{slug}/slides")})
	operation := document["paths"].(map[string]any)["/api/v1/pages/{slug}/slides"].(map[string]any)["get"].(map[string]any)
	response := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/SlidesRead" {
		t.Fatalf("read lacks named contract: %#v", response)
	}
	schema := document["components"].(map[string]any)["schemas"].(map[string]any)["SlidesRead"].(map[string]any)
	if schema["additionalProperties"] != false {
		t.Fatal("Slides read must be a closed projection")
	}
	props := schema["properties"].(map[string]any)
	if len(props) != 6 {
		t.Fatalf("read projects %d fields, want6", len(props))
	}
	version := props["schema_version"].(map[string]any)["enum"].([]int)
	if len(version) != 1 || version[0] != 1 {
		t.Fatal("public reader advertises unsupported schema")
	}
}

func TestSlidesHumanInitializeHasNamedReceiptContract(t *testing.T) {
	document := buildDocument([]route{routeFromKey("POST /api/v1/pages/{slug}/slides/initialize")})
	op := document["paths"].(map[string]any)["/api/v1/pages/{slug}/slides/initialize"].(map[string]any)["post"].(map[string]any)
	request := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if request["$ref"] != "#/components/schemas/SlidesInitialize" {
		t.Fatalf("initialize request lacks named contract: %#v", request)
	}
	response := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/SlidesReceipt" {
		t.Fatalf("initialize response lacks native receipt contract: %#v", response)
	}
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"SlidesInitialize", "SlidesReceipt"} {
		schema := components[name].(map[string]any)
		if schema["additionalProperties"] != false || len(schema["properties"].(map[string]any)) != 5 {
			t.Fatalf("%s must have five closed fields", name)
		}
	}
}
