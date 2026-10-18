package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPresetMetadataCLIReadEditSet(t *testing.T) {
	stub := covStub(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	path := "/api/v1/workspaces/" + covWSCli3 + "/members/m-viewer/access"
	payload := `{"membership_id":"m-viewer","revision":7,"mode":"restricted","rights":[],"preset_selections":[{"id":"saved","kind":"project","resource_id":"project","preset_id":"project-levels","version":1,"level":"view","definition_hash":"historical","selected_by":"historical-actor","selected_at":"original","state":"custom"}]}`
	stub.OnGet(path, func(*http.Request, []byte) (int, []byte, string) { return 200, []byte(payload), "application/json" })
	out := covCaptureAll(t, func() {
		if err := workspaceMemberAccessGetCmd.RunE(newFlagCmd(nil, nil), []string{"u-viewer"}); err != nil {
			t.Error(err)
		}
	})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &decoded); err != nil || !strings.Contains(string(decoded["preset_selections"]), "historical-actor") {
		t.Fatalf("CLI dropped readonly provenance:%s %v", out, err)
	}
	stub.OnPut(path, func(_ *http.Request, b []byte) (int, []byte, string) {
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(b, &sent); err != nil || string(sent["revision"]) != "7" || !strings.Contains(string(sent["preset_selections"]), "historical-actor") {
			t.Fatalf("round-trip changed policy:%s %v", b, err)
		}
		return 200, []byte(payload), "application/json"
	})
	cmd := newFlagCmd(map[string]string{"file": "-"}, nil)
	cmd.SetIn(strings.NewReader(out))
	covCaptureAll(t, func() {
		if err := workspaceMemberAccessSetCmd.RunE(cmd, []string{"u-viewer"}); err != nil {
			t.Error(err)
		}
	})
	if len(stub.CallsFor("PUT", path)) != 1 {
		t.Fatal("read-edit-set did not submit exactly once")
	}
}
