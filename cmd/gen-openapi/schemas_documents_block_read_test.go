package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
)

func TestDocumentBlockReadContractMatchesMandatoryWire(t *testing.T) {
	response := documentBlockReadSchema()
	block := response["properties"].(map[string]any)["block"].(map[string]any)
	for name, tc := range map[string]struct {
		schema map[string]any
		dto    any
	}{"response": {response, api.DocumentBlockReadResponse{}}, "block": {block, api.DocumentOutlineBlock{}}} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(tc.dto)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if json.Unmarshal(raw, &wire) != nil {
				t.Fatal("invalid wire")
			}
			props := tc.schema["properties"].(map[string]any)
			required := tc.schema["required"].([]string)
			got := map[string]bool{}
			want := map[string]bool{}
			for key := range wire {
				want[key] = true
			}
			for _, key := range required {
				got[key] = true
			}
			if !reflect.DeepEqual(got, want) || len(props) != len(wire) || tc.schema["additionalProperties"] != false {
				t.Fatalf("contract fields=%v required=%v actual=%v", props, got, want)
			}
		})
	}
	operation := documentSchemaCatalog()["GET /api/v1/pages/{slug}/document"]
	if len(operation.Response["oneOf"].([]any)) != 2 || len(operation.Parameters) != 1 || operation.Parameters[0]["name"] != "block_id" || operation.Parameters[0]["required"] != false {
		t.Fatal("current block selection replaced whole document read or lost optional query")
	}
	found := false
	for _, status := range operation.ErrorStatuses {
		if status == "422" {
			found = true
		}
	}
	if !found {
		t.Fatal("bounded text/unsupported target status undocumented")
	}
}
