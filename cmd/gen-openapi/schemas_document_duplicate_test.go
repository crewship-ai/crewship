package main

import "testing"

func TestDocumentDuplicateContractIsClosedAndIdempotent(t *testing.T) {
	contract, ok := documentSchemaCatalog()["POST /api/v1/pages/{id}/duplicate"]
	if !ok || !contract.RequestRequired || contract.Response["$ref"] != "#/components/schemas/DocumentDuplicate" {
		t.Fatal("missing closed duplicate contract")
	}
	if len(contract.Parameters) != 1 || contract.Parameters[0]["required"] != true || contract.Parameters[0]["name"] != "Idempotency-Key" {
		t.Fatal("stable human action header must be required")
	}
	request := contract.Request
	if request["additionalProperties"] != false || len(request["required"].([]string)) != 2 {
		t.Fatal("copy packet is not closed")
	}
	props := request["properties"].(map[string]any)
	source := props["expected_source"].(map[string]any)
	if source["additionalProperties"] != false || len(source["required"].([]string)) != 2 {
		t.Fatal("source tuple is not closed")
	}
	version := source["properties"].(map[string]any)["pages_version"].(map[string]any)
	if version["nullable"] != true {
		t.Fatal("null must explicitly request server latest, not a fabricated Document revision")
	}
	response := documentSchemaComponents()["DocumentDuplicate"].(map[string]any)
	if response["additionalProperties"] != false {
		t.Fatal("copy receipt discloses unsupported data")
	}
	page := response["properties"].(map[string]any)["page"].(map[string]any)
	if page["additionalProperties"] != false || len(page["properties"].(map[string]any)) != 2 {
		t.Fatal("copy response must contain only immutable ID and slug")
	}
}
