package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestPageCLIContentReferencesSurviveMachineOutput(t *testing.T) {
	stub := pageStub(t)
	reference := map[string]any{"type_id": "future", "schema_version": float64(42), "supported": false, "reason": "unsupported-version"}
	app := map[string]any{"type_id": "application", "schema_version": float64(1), "supported": true}
	row := map[string]any{"slug": "registry", "content_descriptor": reference, "application_descriptor": app}
	detail, _ := json.Marshal(row)
	list, _ := json.Marshal([]any{row})
	stub.OnGet("/api/v1/pages/registry", func(*http.Request, []byte) (int, []byte, string) { return 200, detail, "application/json" })
	stub.OnGet("/api/v1/pages", func(*http.Request, []byte) (int, []byte, string) { return 200, list, "application/json" })
	for _, args := range [][]string{{"page", "get", "registry", "--format", "json"}, {"page", "list", "--format", "json"}} {
		output, err := runPageCLI(t, "", args...)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal([]byte(output), &value); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]any{"content_descriptor": reference, "application_descriptor": app} {
			actual, ok := jsonFind(value, key)
			if !ok || !reflect.DeepEqual(actual, want) {
				t.Fatalf("%v lost %s: %s", args, key, output)
			}
		}
	}
}
