package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
)

func TestDocumentContractsDescribeActualWireFields(t *testing.T) {
	components := documentSchemaComponents()
	for name, dto := range map[string]any{"DocumentNode": api.DocumentNode{}, "DocumentState": api.DocumentStateResponse{}, "DocumentReceipt": api.DocumentMutationResponse{}, "DocumentInitializationState": api.DocumentInitializationState{}, "DocumentInitializeReceipt": api.DocumentInitializeResponse{}} {
		props := components[name].(map[string]any)["properties"].(map[string]any)
		typ := reflect.TypeOf(dto)
		if len(props) != typ.NumField() {
			t.Fatalf("%s fields=%d wire=%d", name, len(props), typ.NumField())
		}
		for i := 0; i < typ.NumField(); i++ {
			field := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if _, ok := props[field]; !ok {
				t.Errorf("%s missing actual field %s", name, field)
			}
		}
	}
}
func TestDocumentContractsPreserveCASAndDistinctRawAuthority(t *testing.T) {
	catalog := documentSchemaCatalog()
	patch := catalog["POST /api/v1/pages/{slug}/document/patch"]
	variants := patch.Request["oneOf"].([]any)
	human := variants[0].(map[string]any)
	semantic := variants[1].(map[string]any)
	for _, field := range []string{"id", "schema", "epoch", "target", "text", "ifRev", "ifHash"} {
		found := false
		for _, required := range semantic["required"].([]string) {
			found = found || required == field
		}
		if !found {
			t.Errorf("semantic patch missing required %s", field)
		}
	}
	humanProps := human["properties"].(map[string]any)
	if humanProps["kind"].(map[string]any)["enum"].([]string)[0] != "human" {
		t.Fatal("raw update schema lost human-only authority")
	}
	if human["additionalProperties"] != false || semantic["additionalProperties"] != false {
		t.Fatal("patch contracts accept invented fields")
	}
	if catalog["GET /api/v1/pages/{slug}/document/export"].ResponseMedia[0] != "text/markdown" {
		t.Fatal("export incorrectly claims JSON")
	}
	if catalog["POST /api/v1/pages/{slug}/document/requests"].SuccessStatuses[0] != "202" {
		t.Fatal("queue receipt incorrectly declares completion")
	}
	for key, operation := range catalog {
		if operation.Response == nil {
			t.Errorf("%s missing explicit response", key)
		}
		if strings.HasPrefix(key, "POST ") && (!operation.RequestRequired || operation.Request == nil) {
			t.Errorf("%s missing mandatory typed request", key)
		}
	}
}

func TestDocumentInitializeFirstStateAndRetryReceiptRemainDistinct(t *testing.T) {
	operation := documentSchemaCatalog()["POST /api/v1/pages/{slug}/document/initialize"]
	branches := operation.Response["oneOf"].([]any)
	if len(branches) != 2 || branches[0].(map[string]any)["$ref"] != "#/components/schemas/DocumentInitializationState" || branches[1].(map[string]any)["$ref"] != "#/components/schemas/DocumentInitializeReceipt" {
		t.Fatalf("initialization variants=%v", branches)
	}
	components := documentSchemaComponents()
	initial := components["DocumentInitializationState"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"id", "actor", "kind"} {
		if _, exists := initial[field]; exists {
			t.Errorf("initial state claims receipt-only field %s", field)
		}
	}
	if initial["epoch"].(map[string]any)["type"] != "integer" {
		t.Fatal("initialization state epoch must match numeric durable fence")
	}
	comment := documentSchemaCatalog()["POST /api/v1/pages/{slug}/document/comments"].Request["properties"].(map[string]any)["text"].(map[string]any)
	if comment["maxLength"] != 10000 {
		t.Fatalf("comment limit=%v", comment)
	}
}
