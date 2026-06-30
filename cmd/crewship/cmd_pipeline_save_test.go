package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// routineSaveMock simulates the server contract for `routine save`
// (issue #654). The internal save route behaves like production:
// it is mounted under internalAuth, so a user CLI token always gets
// 403 there. The CLI must therefore complete the save through the
// user-facing workspace-scoped endpoint. The public test_run surface
// was removed — the save goes straight to /pipelines/save (the server
// validates the DSL on save) with the body-trust gate fields.
type routineSaveMock struct {
	t                  *testing.T
	testRunCalled      bool
	userSaveCalled     bool
	gotLastTestRunPass bool
	gotSlug            string
	gotAuthorCrew      string
	internalSaveHits   int
}

func (m *routineSaveMock) handler() http.Handler {
	mux := http.NewServeMux()
	// The public test_run route is gone — flag it if the CLI still calls it.
	mux.HandleFunc("POST /api/v1/workspaces/ws_test_1/pipelines/test_run", func(w http.ResponseWriter, r *http.Request) {
		m.testRunCalled = true
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /api/v1/workspaces/ws_test_1/pipelines/save", func(w http.ResponseWriter, r *http.Request) {
		m.userSaveCalled = true
		var body struct {
			Slug              string          `json:"slug"`
			Definition        json.RawMessage `json:"definition"`
			AuthorCrewID      string          `json:"author_crew_id"`
			LastTestRunPassed bool            `json:"last_test_run_passed"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.gotLastTestRunPass = body.LastTestRunPassed
		m.gotSlug = body.Slug
		m.gotAuthorCrew = body.AuthorCrewID
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              "pln_test",
			"slug":            body.Slug,
			"name":            body.Slug,
			"definition_hash": "abcdef1234567890",
		})
	})
	// Production mounts this under internalAuth — a user JWT/CLI
	// token is always rejected. Pre-#654 the CLI posted here.
	mux.HandleFunc("POST /api/v1/internal/pipelines/save", func(w http.ResponseWriter, r *http.Request) {
		m.internalSaveHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Forbidden"})
	})
	return mux
}

// TestRoutineSave_UsesUserFacingEndpoint pins the #654 fix: the save
// flow must hit the workspace-scoped /pipelines/save and never touch the
// internal-auth route nor the removed public test_run route.
func TestRoutineSave_UsesUserFacingEndpoint(t *testing.T) {
	saveCLIState(t)

	m := &routineSaveMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "user-token", Workspace: "ws_test_1", Server: srv.URL}

	defPath := filepath.Join(t.TempDir(), "probe.json")
	def := `{"dsl_version":"1.0","name":"save-test-probe","agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`
	if err := os.WriteFile(defPath, []byte(def), 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}

	set := func(flag, val string) {
		if err := pipelineSaveCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	set("name", "save-test-probe")
	set("definition", defPath)
	set("author-crew", "crew_test_1")
	t.Cleanup(func() {
		set("name", "")
		set("definition", "")
		set("author-crew", "")
	})

	if err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil); err != nil {
		t.Fatalf("routine save failed: %v", err)
	}

	if m.testRunCalled {
		t.Error("save must NOT call the removed public test_run route")
	}
	if !m.userSaveCalled {
		t.Error("user-facing /pipelines/save was never invoked")
	}
	if m.internalSaveHits != 0 {
		t.Errorf("CLI hit the internal-auth save route %d time(s) — that path 403s for user tokens (issue #654)", m.internalSaveHits)
	}
	if !m.gotLastTestRunPass {
		t.Error("save body must set last_test_run_passed=true (body-trust gate)")
	}
	if m.gotSlug != "save-test-probe" {
		t.Errorf("slug: got %q", m.gotSlug)
	}
	if m.gotAuthorCrew != "crew_test_1" {
		t.Errorf("author_crew_id: got %q", m.gotAuthorCrew)
	}
}
