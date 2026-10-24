package sidecar

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Validate complete client packets against the actual advertised MCP catalogs,
// not a local schema fixture. A typed HTTP decoder accepting a field is not
// sufficient when a conforming MCP client is forbidden to submit it.
func TestWorkbookReadSetsAllowedByActualMCPCatalogs(t *testing.T) {
	for _, tool := range append(gridcraftMCPCatalog(), mooringMCPCatalog()...) {
		if tool.Name != "workbook_apply" && tool.Name != "mooring_command" {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			schema, err := jsonschema.CompileString("catalog.json", string(tool.InputSchema))
			if err != nil {
				t.Fatal(err)
			}
			cell := map[string]any{"sheet": "Sheet1", "cell": "A1", "rev": 1}
			region := map[string]any{"sheet": "Sheet1", "start": "A1", "end": "C9", "rev": 1, "structure_rev": 1}
			repeat := func(value any, count int) []any {
				values := make([]any, count)
				for i := range values {
					values[i] = value
				}
				return values
			}
			for _, test := range []struct {
				name  string
				reads any
				valid bool
			}{
				{"legacy omitted", nil, true},
				{"constant assignment", map[string]any{}, true},
				{"observed cell and range", map[string]any{"cells": []any{cell}, "ranges": []any{region}}, true},
				{"bounds inclusive", map[string]any{"cells": repeat(cell, 256), "ranges": repeat(region, 32)}, true},
				{"too many cells", map[string]any{"cells": repeat(cell, 257)}, false},
				{"too many ranges", map[string]any{"ranges": repeat(region, 33)}, false},
				{"missing structure revision", map[string]any{"ranges": []any{map[string]any{"sheet": "Sheet1", "start": "A1", "end": "C9", "rev": 1}}}, false},
				{"invalid cell", map[string]any{"cells": []any{map[string]any{"sheet": "Sheet1", "cell": "a0", "rev": 1}}}, false},
				{"zero observation revision", map[string]any{"cells": []any{map[string]any{"sheet": "Sheet1", "cell": "A1", "rev": 0}}}, false},
				{"unknown observation field", map[string]any{"cells": []any{map[string]any{"sheet": "Sheet1", "cell": "A1", "rev": 1, "trusted": true}}}, false},
				{"unknown read set field", map[string]any{"trusted": true}, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					request := map[string]any{"mutation_id": "retained", "base_rev": 1, "generation": 1}
					arguments := map[string]any{"artifact_id": "page", "mandate_id": "mandate", "request": request}
					if tool.Name == "workbook_apply" {
						request["mandate_id"] = "mandate"
						request["command"] = map[string]any{"kind": "cell.set", "sheet": "Sheet1", "cell": "B1", "input": "42"}
					} else {
						arguments["action"] = "command"
						request["command"] = "cell.set"
						request["arguments"] = map[string]any{"sheet": "Sheet1", "cell": "B1", "input": "42"}
					}
					if test.reads != nil {
						request["read_set"] = test.reads
					}
					// JSON decoding matches the real MCP client representation.
					encoded, _ := json.Marshal(arguments)
					var decoded any
					if err := json.Unmarshal(encoded, &decoded); err != nil {
						t.Fatal(err)
					}
					if err := schema.Validate(decoded); (err == nil) != test.valid {
						t.Fatalf("valid=%v: %v", test.valid, err)
					}
				})
			}
		})
	}
}
