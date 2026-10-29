package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAuditRotateKeyCLIBodyAndRestartNotice(t *testing.T) {
	saveCLIState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/admin/audit/signing-keys/rotate" || r.URL.Query().Get("workspace_id") != "" {
			t.Errorf("unexpected route %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["reason"] != "Scheduled key rotation" || body["expected_active_key_id"] != "key-old" || len(body) != 2 {
			t.Errorf("rotation body: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"previous_active_key_id":"key-old","active_key_id":"key-new","effective":"next_start"}`))
	}))
	defer server.Close()
	cliCfg = &cli.CLIConfig{Token: "synthetic-unit-token", Server: server.URL, Workspace: "cabcdefghijklmnopqrs"}
	flagWorkspace = ""
	cmd := newAuditRotateKeyCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--reason=Scheduled key rotation", "--expected-active-key-id=key-old"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("key-new")) || !bytes.Contains(out.Bytes(), []byte("Restart the server")) {
		t.Fatalf("CLI output hid the new key or the restart: %s", out.String())
	}
}
