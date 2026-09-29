package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMemberAccessGeneratedContract(t *testing.T) {
	raw, err := os.ReadFile("../../internal/api/openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err = json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	path := paths["/api/v1/workspaces/{workspaceId}/members/{memberId}/access"].(map[string]any)
	put := path["put"].(map[string]any)
	if put["requestBody"].(map[string]any)["required"] != true {
		t.Fatal("replacement body is optional")
	}
	for _, method := range []string{"get", "put"} {
		op := path[method].(map[string]any)
		responses := op["responses"].(map[string]any)
		if responses["404"] == nil || responses["500"] == nil || (method == "put" && responses["409"] == nil) {
			t.Fatalf("missing policy error contracts: %s", method)
		}
		schema := responses["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if len(schema["required"].([]any)) != 4 {
			t.Fatal("incomplete policy envelope")
		}
		rights := schema["properties"].(map[string]any)["rights"].(map[string]any)
		if rights["type"] != "array" || rights["nullable"] == true || rights["minItems"] != nil {
			t.Fatal("empty deny-all array contract changed")
		}
	}
}
