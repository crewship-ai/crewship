package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAuditHeadCLIIsSeparateReadOnlyOwnerAction(t *testing.T) {
	saveCLIState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/admin/audit/head" || r.URL.Query().Get("scope") != "instance" || r.URL.Query().Get("workspace_id") != "" {
			t.Error("head command changed scope or attempted a mutation")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tail_seq":7,"tail_hash":"reviewable-native-tail"}`))
	}))
	defer server.Close()
	cliCfg = &cli.CLIConfig{Token: "synthetic-unit-token", Server: server.URL, Workspace: "cabcdefghijklmnopqrs"}
	flagWorkspace = ""
	cmd := newAuditHeadCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--scope=instance"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("reviewable-native-tail")) || !bytes.Contains(out.Bytes(), []byte("does not accept or reseal")) {
		t.Fatal("head command hid CAS input or implied history acceptance")
	}
}
