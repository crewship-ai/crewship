package main

import (
	"reflect"
	"testing"
)

func TestCraftNativeDocumentsHaveConcreteNativeBindingsAndStringVersions(t *testing.T) {
	prefix := "/api/v1/pages/{slug}/craft/document"
	doc := buildDocument([]route{{method: "GET", path: prefix}, {method: "POST", path: prefix + "/save"}, {method: "POST", path: prefix + "/import"}, {method: "POST", path: prefix + "/restore"}, {method: "POST", path: prefix + "/reconcile"}})
	paths := doc["paths"].(map[string]any)
	read := paths[prefix].(map[string]any)["get"].(map[string]any)
	if responseSchemaFromOperation(t, read)["$ref"] != "#/components/schemas/NativeCraftDocument" {
		t.Fatal("document read has no concrete wire schema")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	request, ok := schemas["NativeCraftSnapshotRequest"].(map[string]any)
	if !ok {
		t.Fatal("native request binding absent")
	}
	props := request["properties"].(map[string]any)
	for _, key := range []string{"baseVersion", "generation"} {
		if props[key].(map[string]any)["type"] != "string" {
			t.Fatalf("%s loses int64 precision", key)
		}
	}
	if request["additionalProperties"] != false {
		t.Fatal("request could choose unknown validator or credential fields")
	}
	if props["bytes"].(map[string]any)["maxLength"] != 44739244 {
		t.Fatal("native32MiB base64 bound missing")
	}
	for _, suffix := range []string{"/save", "/import", "/reconcile"} {
		op := paths[prefix+suffix].(map[string]any)["post"].(map[string]any)
		if responseSchemaFromOperation(t, op)["$ref"] != "#/components/schemas/NativeCraftSnapshotReceipt" {
			t.Fatalf("receipt schema missing for %s", suffix)
		}
	}
}

func TestCraftNativeInitializeHasOwnerNativeTypedAdmission(t *testing.T) {
	const path = "/api/v1/pages/{slug}/craft/initialize"
	doc := buildDocument([]route{{method: "POST", path: path}})
	operation := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	if operation["responses"].(map[string]any)["201"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"] != "#/components/schemas/NativeCraftInitializeReceipt" {
		t.Fatal("initializer has no concrete admission response")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	request := schemas["NativeCraftInitializeRequest"].(map[string]any)
	props := request["properties"].(map[string]any)
	if len(props) != 2 || request["additionalProperties"] != false {
		t.Fatal("initializer accepts client scope or qualification fields")
	}
	if !reflect.DeepEqual(props["application_id"].(map[string]any)["enum"], []string{"wordcraft", "gridcraft"}) {
		t.Fatal("initializer does not list its supported native applications")
	}
}

func TestCraftNativeProposalsHaveConcreteSchemas(t *testing.T) {
	base := "/api/v1/pages/{slug}/craft/proposals"
	doc := buildDocument([]route{{method: "GET", path: base}, {method: "GET", path: base + "/{proposalID}"}, {method: "POST", path: base + "/{proposalID}/accept"}, {method: "POST", path: base + "/{proposalID}/reject"}})
	paths := doc["paths"].(map[string]any)
	for path, want := range map[string]string{base: "NativeCraftProposalList", base + "/{proposalID}": "NativeCraftProposalDocument"} {
		if responseSchemaFromOperation(t, paths[path].(map[string]any)["get"].(map[string]any))["$ref"] != "#/components/schemas/"+want {
			t.Fatalf("%s has no concrete response", path)
		}
	}
	for suffix, want := range map[string]string{"/accept": "NativeCraftProposalAcceptReceipt", "/reject": "NativeCraftProposal"} {
		if responseSchemaFromOperation(t, paths[base+"/{proposalID}"+suffix].(map[string]any)["post"].(map[string]any))["$ref"] != "#/components/schemas/"+want {
			t.Fatalf("%s has no concrete response", suffix)
		}
	}
	accept := doc["components"].(map[string]any)["schemas"].(map[string]any)["NativeCraftProposalAcceptRequest"].(map[string]any)
	if accept["additionalProperties"] != false || accept["properties"].(map[string]any)["generation"].(map[string]any)["type"] != "string" {
		t.Fatal("accept request must be closed and keep the generation as a string")
	}
}
