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
	gotTestRunCrew   string
	gotDefinition    json.RawMessage
	internalSaveHits int
}

func (m *routineSaveMock) handler() http.Handler {
	mux := http.NewServeMux()
	// Crew listing — resolveCrewID maps slugs to CUIDs through this route.
	mux.HandleFunc("GET /api/v1/crews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "ccrewengineering00001", "slug": "engineering"},
		})
	})
	// Single-crew verify — resolveCrewID's #1075 CUID check GETs this before
	// trusting a CUID-shaped --author-crew. The known id is "real" (200) so
	// resolution short-circuits and forwards it verbatim; anything else 404s
	// and resolution falls back to the slug scan on the list route above.
	mux.HandleFunc("GET /api/v1/crews/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "ccrewtest000000000001" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "ccrewtest000000000001", "slug": "audit"})
	})
	// Public draft-validation gate — dry-run validates + mints the save_token.
	mux.HandleFunc("POST /api/v1/workspaces/ws_test_1/pipelines/test_run", func(w http.ResponseWriter, r *http.Request) {
		m.testRunCalled = true
		var body struct {
			AuthorCrewID string `json:"author_crew_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.gotTestRunCrew = body.AuthorCrewID
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
		m.gotDefinition = body.Definition
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
	set("author-crew", "ccrewtest000000000001")
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
	if m.gotAuthorCrew != "ccrewtest000000000001" {
		t.Errorf("author_crew_id: got %q", m.gotAuthorCrew)
	}
}

// TestRoutineSave_ResolvesAuthorCrewSlug pins the #997 fix: --author-crew
// accepts a crew slug, resolved to its CUID before the test_run/save calls —
// the save endpoints bind author_crew_id by ID only, so a raw slug used to
// 403 with a misleading "crew does not belong to this workspace".
func TestRoutineSave_ResolvesAuthorCrewSlug(t *testing.T) {
	saveCLIState(t)

	m := &routineSaveMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "user-token", Workspace: "ws_test_1", Server: srv.URL}

	defPath := filepath.Join(t.TempDir(), "probe.json")
	def := `{"dsl_version":"1.0","name":"save-slug-probe","agentless":true,"steps":[{"id":"t","type":"transform","transform":{"input":"true","expression":"."}}]}`
	if err := os.WriteFile(defPath, []byte(def), 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}

	set := func(flag, val string) {
		if err := pipelineSaveCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	set("name", "save-slug-probe")
	set("definition", defPath)
	set("author-crew", "engineering")
	t.Cleanup(func() {
		set("name", "")
		set("definition", "")
		set("author-crew", "")
	})

	if err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil); err != nil {
		t.Fatalf("routine save failed: %v", err)
	}

	if m.gotTestRunCrew != "ccrewengineering00001" {
		t.Errorf("test_run author_crew_id: got %q, want the resolved CUID", m.gotTestRunCrew)
	}
	if m.gotAuthorCrew != "ccrewengineering00001" {
		t.Errorf("save author_crew_id: got %q, want the resolved CUID", m.gotAuthorCrew)
	}
}

// TestRoutineSave_AcceptsYAMLDefinition pins #1423 item 2 through the full
// `routine save` path: a --definition file written as YAML (comments, a
// literal block-scalar prompt) is converted to canonical JSON before
// either the test_run or the save request body is built, so the server —
// mocked here — sees the same JSON it always has, with the multiline
// prompt intact (not JSON-escaped, not truncated).
func TestRoutineSave_AcceptsYAMLDefinition(t *testing.T) {
	saveCLIState(t)

	m := &routineSaveMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "user-token", Workspace: "ws_test_1", Server: srv.URL}

	defPath := filepath.Join(t.TempDir(), "probe.yaml")
	def := `
# authored as YAML — comments + a real multiline prompt
dsl_version: "1.0"
name: save-yaml-probe
steps:
  - id: a
    type: agent_run
    agent_slug: x
    prompt: |
      Line one.
      Line two.
`
	if err := os.WriteFile(defPath, []byte(def), 0o600); err != nil {
		t.Fatalf("write definition: %v", err)
	}

	set := func(flag, val string) {
		if err := pipelineSaveCmd.Flags().Set(flag, val); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	set("name", "save-yaml-probe")
	set("definition", defPath)
	set("author-crew", "ccrewtest000000000001")
	t.Cleanup(func() {
		set("name", "")
		set("definition", "")
		set("author-crew", "")
	})

	if err := pipelineSaveCmd.RunE(pipelineSaveCmd, nil); err != nil {
		t.Fatalf("routine save failed: %v", err)
	}

	if !json.Valid(m.gotDefinition) {
		t.Fatalf("definition sent to /save is not valid JSON: %s", m.gotDefinition)
	}
	var sent struct {
		Name  string `json:"name"`
		Steps []struct {
			Prompt string `json:"prompt"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(m.gotDefinition, &sent); err != nil {
		t.Fatalf("decode definition: %v", err)
	}
	if sent.Name != "save-yaml-probe" {
		t.Errorf("name: got %q", sent.Name)
	}
	if len(sent.Steps) != 1 || sent.Steps[0].Prompt != "Line one.\nLine two.\n" {
		t.Errorf("multiline prompt not preserved through YAML→JSON conversion: %+v", sent.Steps)
	}
}
