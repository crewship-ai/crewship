package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPresetCLICatalogStructuredOutput(t *testing.T) {
	for _, format := range []string{"json", "yaml", "ndjson", "table"} {
		t.Run(format, func(t *testing.T) {
			stub := covStub(t)
			flagFormat = format
			path := "/api/v1/workspaces/" + covWSCli3 + "/access/presets"
			stub.OnGet(path, func(r *http.Request, _ []byte) (int, []byte, string) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("credential changed")
				}
				return 200, []byte(`{"presets":[{"definition":{"preset_id":"project-levels","version":1,"kind":"project","managed_operations":["list","read"],"levels":[{"level":"view","operations":["list","read"]}]},"definition_hash":"immutable-hash"}]}`), "application/json"
			})
			out := covCaptureAll(t, func() {
				if err := workspaceAccessPresetsCmd.RunE(newFlagCmd(nil, nil), nil); err != nil {
					t.Error(err)
				}
			})
			if !strings.Contains(out, "project-levels") || !strings.Contains(out, "immutable-hash") || len(stub.CallsFor("GET", path)) != 1 {
				t.Fatalf("incomplete catalog:%s", out)
			}
			if format == "yaml" {
				var document map[string]any
				if err := yaml.Unmarshal([]byte(out), &document); err != nil {
					t.Fatal(err)
				}
				entries, ok := document["presets"].([]any)
				if !ok || len(entries) != 1 {
					t.Fatalf("YAML catalog envelope:%s", out)
				}
				entry := entries[0].(map[string]any)
				definition := entry["definition"].(map[string]any)
				if entry["definition_hash"] != "immutable-hash" || definition["preset_id"] != "project-levels" || definition["managed_operations"] == nil {
					t.Fatalf("YAML catalog does not preserve API field names:%s", out)
				}
			}
			if (format == "json" || format == "ndjson") && !json.Valid([]byte(out)) {
				t.Fatalf("invalid machine-readable catalog:%s", out)
			}
		})
	}
}
