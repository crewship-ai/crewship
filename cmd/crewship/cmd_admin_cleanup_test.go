//go:build !clionly

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestAdminCleanupDoesNotRequireSelectedWorkspace(t *testing.T) {
	saveCLIState(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/resource-cleanup" || r.Header.Get("Authorization") != "Bearer cleanup-test-token" {
			t.Errorf("unexpected cleanup request: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("workspace_id") != "" {
			t.Error("instance cleanup request selected a workspace")
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"scope":"containers","state":"enabled","items":[]}`)
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Server: srv.URL, Token: "cleanup-test-token"}
	flagServer, flagWorkspace = "", ""
	cmd := newAdminCleanupCmd()
	cmd.SetOut(io.Discard)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("cleanup without a workspace: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("cleanup did not read server diagnostics")
	}
}
