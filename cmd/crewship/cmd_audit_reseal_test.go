package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAuditResealCLIExactTailAndHistoricalWarning(t *testing.T) {
	saveCLIState(t)
	const ws = "cabcdefghijklmnopqrs"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/admin/audit/reseal" {
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["scope"] != "instance" || body["workspace_id"] != nil || r.URL.Query().Get("workspace_id") != "" {
			t.Error("instance checkpoint inherited workspace scope")
		}
		if body["expected_tail_seq"] != float64(7) || body["expected_tail_hash"] != "reviewed-tail" || body["reason"] != "Approved local test recovery" {
			t.Error("checkpoint lost reviewed CAS or reason")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scope":"instance","checkpoint_id":"checkpoint-test","tail_seq":8,"tail_hash":"new-tail","continuation_verified":true,"historical_status":"historical_unverified"}`))
	}))
	defer server.Close()
	cliCfg = &cli.CLIConfig{Token: "synthetic-unit-token", Server: server.URL, Workspace: ws}
	flagWorkspace = ""
	cmd := newAuditResealCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--scope=instance", "--expected-tail-seq=7", "--expected-tail-hash=reviewed-tail", "--reason=Approved local test recovery"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("historical_unverified")) || !bytes.Contains(out.Bytes(), []byte("not rewritten")) {
		t.Fatal("CLI hid unverified historical status")
	}
}
