package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMooringProvenanceBinaryUsesOriginalHumanAndStablePage(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "GET" || r.URL.Path != "/api/v1/pages/@original-page/mooring/provenance" || r.URL.Query().Get("workspace_id") != "cmooringworkspace000000001" || r.Header.Get("Authorization") != "Bearer synthetic-human" {
			t.Error("provenance route/identity changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"artifacts":[],"artifacts_partial":true}`))
	}))
	defer server.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("server: %s\ntoken: synthetic-human\nworkspace: cmooringworkspace000000001\nformat: json\n", server.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), buildCrewshipBinary(t), "mooring", "provenance", "@original-page", "-f", "json")
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CREWSHIP_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+cfg, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if err != nil || hits != 1 || !strings.Contains(string(out), "artifacts_partial") {
		t.Fatal(err, string(out), hits)
	}
}
