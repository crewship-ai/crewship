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
// (issue #654). The internal save route behaves like production: it is mounted
// under internalAuth, so a user CLI token always gets 403 there. The CLI must
// complete the save through the user-facing workspace-scoped endpoints. The
// flow is two steps: POST /pipelines/test_run dry-run-validates the draft and
// mints an HMAC save_token; POST /pipelines/save then clears its test-gate with
// that token (the server no longer trusts a body "it passed" claim).
type routineSaveMock struct {
	t                *testing.T
	testRunCalled    bool
	userSaveCalled   bool
	gotSaveToken     string
	gotSlug          string
	gotAuthorCrew    string
	internalSaveHits int
}

func (m *routineSaveMock) handler() http.Handler {
	mux := http.NewServeMux()
	// Public draft-validation gate — dry-run validates + mints the save_token.
	mux.HandleFunc("POST /api/v1/workspaces/ws_test_1/pipelines/test_run", func(w http.ResponseWriter, r *http.Request) {
		m.testRunCalled = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":     "DRY_RUN_OK",
			"save_token": "stub-save-token",
		})
	})
	mux.HandleFunc("POST /api/v1/workspaces/ws_test_1/pipelines/save", func(w http.ResponseWriter, r *http.Request) {
		m.userSaveCalled = true
		var body struct {
			Slug         string          `json:"slug"`
			Definition   json.RawMessage `json:"definition"`
			AuthorCrewID string          `json:"author_crew_id"`
			SaveToken    string          `json:"save_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.gotSaveToken = body.SaveToken
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
	// Production mounts this under internalAuth — a user JWT/CLI token is always
	// rejected. Pre-#654 the CLI posted here.
	mux.HandleFunc("POST /api/v1/internal/pipelines/save", func(w http.ResponseWriter, r *http.Request) {
		m.internalSaveHits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Forbidden"})
	})
	return mux
}

// TestRoutineSave_UsesUserFacingEndpoint pins the #654 fix plus the restored
// proof flow: the save validates via the public test_run, forwards the minted
// save_token to the workspace-scoped /pipelines/save, and never touches the
// internal-auth route.
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

	if !m.testRunCalled {
		t.Error("save must dry-run-validate via the public test_run route first")
	}
	if !m.userSaveCalled {
		t.Error("user-facing /pipelines/save was never invoked")
	}
	if m.internalSaveHits != 0 {
		t.Errorf("CLI hit the internal-auth save route %d time(s) — that path 403s for user tokens (issue #654)", m.internalSaveHits)
	}
	if m.gotSaveToken != "stub-save-token" {
		t.Errorf("save body must forward the minted save_token; got %q", m.gotSaveToken)
	}
	if m.gotSlug != "save-test-probe" {
		t.Errorf("slug: got %q", m.gotSlug)
	}
	if m.gotAuthorCrew != "crew_test_1" {
		t.Errorf("author_crew_id: got %q", m.gotAuthorCrew)
	}
}
