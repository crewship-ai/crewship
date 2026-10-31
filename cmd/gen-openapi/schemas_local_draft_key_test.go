package main

import (
	"encoding/json"
	"testing"
)

func TestLocalDraftKeyContractIsConcrete(t *testing.T) {
	const path = "/api/v1/pages/{slug}/draft-key"
	doc := buildDocument([]route{{method: "POST", path: path}})
	op := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	request := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if request["$ref"] != "#/components/schemas/LocalDraftKeyRequest" || response["$ref"] != "#/components/schemas/LocalDraftKeyResponse" {
		t.Fatalf("generic recovery contract: request=%v response=%v", request, response)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ schema, good, bad string }{
		{"LocalDraftKeyRequest", `{"human":"alice","operation":"wrap","key":"a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="}`, `{"human":"alice","operation":"wrap","key":"short"}`},
		{"LocalDraftKeyRequest", `{"human":"alice","operation":"unwrap","envelope":"v1:value"}`, `{"operation":"unwrap","envelope":"v1:value"}`},
		{"LocalDraftKeyRequest", `{"human":"alice","operation":"unwrap","envelope":"v1:value","key":""}`, `{"human":"alice","operation":"unwrap","envelope":"v1:value","key":"nonempty"}`},
		{"LocalDraftKeyResponse", `{"envelope":"v1:value"}`, `{"envelope":"v1:value","key":"also leaked"}`},
	} {
		schema := compileAt(t, raw, "/components/schemas/"+tc.schema)
		var good, bad any
		if json.Unmarshal([]byte(tc.good), &good) != nil || json.Unmarshal([]byte(tc.bad), &bad) != nil {
			t.Fatal("invalid fixture")
		}
		if err := schema.Validate(good); err != nil {
			t.Fatal("valid wire rejected", err)
		}
		if err := schema.Validate(bad); err == nil {
			t.Fatal("invalid wire accepted", tc.bad)
		}
	}
}
