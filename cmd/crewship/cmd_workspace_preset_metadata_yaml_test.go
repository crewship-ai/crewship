package main

import (
	"net/http"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPresetMetadataCLIYAMLFieldNames(t *testing.T) {
	stub := covStub(t)
	flagFormat = "yaml"
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access"
	stub.OnGet(path, func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"membership_id":"m-viewer","revision":7,"mode":"restricted","rights":[],"preset_selections":[{"id":"saved","kind":"project","resource_id":"project","preset_id":"project-levels","version":1,"level":"view","definition_hash":"historical","selected_by":"historical-actor","selected_at":"original","state":"custom"}]}`), "application/json"
	})
	out := covCaptureAll(t, func() {
		if err := workspaceMemberAccessGetCmd.RunE(newFlagCmd(nil, nil), []string{"u-viewer"}); err != nil {
			t.Error(err)
		}
	})
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	rows, ok := doc["preset_selections"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("YAML drops new selection field names:%s", out)
	}
	row := rows[0].(map[string]any)
	for key, want := range map[string]any{"resource_id": "project", "preset_id": "project-levels", "definition_hash": "historical", "selected_by": "historical-actor", "selected_at": "original", "version": 1} {
		if row[key] != want {
			t.Fatalf("YAML selection %s=%v want%v: %s", key, row[key], want, out)
		}
	}
}
