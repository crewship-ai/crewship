package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestActivityCmdStructure(t *testing.T) {
	t.Parallel()

	if activityCmd.Use != "activity" {
		t.Errorf("activity Use: got %q want %q", activityCmd.Use, "activity")
	}
	if !strings.Contains(strings.ToLower(activityCmd.Short), "activity") {
		t.Errorf("activity Short should mention activity; got %q", activityCmd.Short)
	}
	if !strings.Contains(activityCmd.Long, "crewship activity") {
		t.Errorf("activity Long should show CLI examples; got %q", activityCmd.Long)
	}
}

func TestActivityCmdFlags(t *testing.T) {
	t.Parallel()

	crew := activityCmd.Flags().Lookup("crew")
	if crew == nil {
		t.Fatal("activity missing --crew flag")
	}
	if crew.DefValue != "" {
		t.Errorf("--crew default: got %q want empty", crew.DefValue)
	}

	lines := activityCmd.Flags().Lookup("lines")
	if lines == nil {
		t.Fatal("activity missing --lines flag")
	}
	if lines.DefValue != "50" {
		t.Errorf("--lines default: got %q want %q", lines.DefValue, "50")
	}
	if lines.Value.Type() != "int" {
		t.Errorf("--lines type: got %q want int", lines.Value.Type())
	}
}

func TestActivityRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestActivityRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// activityMock captures the activity URL served and optionally responds to
// /api/v1/crews so the slug→id resolution can be exercised.
type activityMock struct {
	t          *testing.T
	activityMu sync.Mutex
	activity   string // last /api/v1/activity URL
	crews      []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	activityStatus int
}

func (m *activityMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/activity"):
			m.activityMu.Lock()
			m.activity = r.URL.RequestURI()
			m.activityMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if m.activityStatus != 0 {
				w.WriteHeader(m.activityStatus)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "boom"})
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/crews":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(m.crews)
		default:
			m.t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestActivityRunE_OmitsCrewFilterWhenEmpty(t *testing.T) {
	saveCLIState(t)

	m := &activityMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := activityCmd.Flags().Set("lines", "10"); err != nil {
		t.Fatalf("set --lines: %v", err)
	}
	if err := activityCmd.Flags().Set("crew", ""); err != nil {
		t.Fatalf("reset --crew: %v", err)
	}
	t.Cleanup(func() { _ = activityCmd.Flags().Set("lines", "50") })

	if err := activityCmd.RunE(activityCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !strings.Contains(m.activity, "limit=10") {
		t.Errorf("limit not propagated: %q", m.activity)
	}
	if strings.Contains(m.activity, "crew_id=") {
		t.Errorf("crew_id should be absent when --crew empty: %q", m.activity)
	}
	if !strings.Contains(m.activity, "workspace_id=cabcdefghijklmnopqrs") {
		t.Errorf("workspace_id not injected: %q", m.activity)
	}
}

// TestActivityRunE_CUIDCrewSkipsResolution verifies the fast path of
// resolveCrewID: if the --crew value already looks like a CUID, the CLI
// does not call /api/v1/crews to resolve it.
func TestActivityRunE_CUIDCrewSkipsResolution(t *testing.T) {
	saveCLIState(t)

	m := &activityMock{t: t}
	// Intentionally empty crews list — any lookup would surface as
	// "crew not found" and fail the test. Absence of that error proves
	// resolveCrewID short-circuited.
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cuid := "ccrewpqrsxyzabcdefghi" // 21 chars: 'c' + 20 alphanumeric, matches cuid2 minimum
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := activityCmd.Flags().Set("lines", "5"); err != nil {
		t.Fatalf("set --lines: %v", err)
	}
	if err := activityCmd.Flags().Set("crew", cuid); err != nil {
		t.Fatalf("set --crew: %v", err)
	}
	t.Cleanup(func() {
		_ = activityCmd.Flags().Set("lines", "50")
		_ = activityCmd.Flags().Set("crew", "")
	})

	if err := activityCmd.RunE(activityCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	if !strings.Contains(m.activity, "crew_id="+cuid) {
		t.Errorf("crew_id should equal the passed CUID: %q", m.activity)
	}
}

// TestActivityRunE_SlugCrewResolvesToID goes through the full resolution path:
// --crew=engineering → GET /api/v1/crews → find slug → URL uses the resolved id.
func TestActivityRunE_SlugCrewResolvesToID(t *testing.T) {
	saveCLIState(t)

	m := &activityMock{
		t: t,
		crews: []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}{
			{ID: "crew-eng-cuid1", Slug: "engineering"},
			{ID: "crew-qa-cuid2", Slug: "quality"},
		},
	}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := activityCmd.Flags().Set("crew", "engineering"); err != nil {
		t.Fatalf("set --crew: %v", err)
	}
	if err := activityCmd.Flags().Set("lines", "25"); err != nil {
		t.Fatalf("set --lines: %v", err)
	}
	t.Cleanup(func() {
		_ = activityCmd.Flags().Set("crew", "")
		_ = activityCmd.Flags().Set("lines", "50")
	})

	if err := activityCmd.RunE(activityCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(m.activity, "crew_id=crew-eng-cuid1") {
		t.Errorf("slug not resolved to id: %q", m.activity)
	}
	if !strings.Contains(m.activity, "limit=25") {
		t.Errorf("limit not propagated: %q", m.activity)
	}
}

func TestActivityRunE_UnknownCrewSlugError(t *testing.T) {
	saveCLIState(t)

	m := &activityMock{t: t} // empty crews list
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = activityCmd.Flags().Set("crew", "does-not-exist")
	t.Cleanup(func() { _ = activityCmd.Flags().Set("crew", "") })

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil {
		t.Fatal("expected 'crew not found' error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should mention the slug; got %v", err)
	}
}

func TestActivityRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	m := &activityMock{t: t, activityStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = activityCmd.Flags().Set("crew", "")
	_ = activityCmd.Flags().Set("lines", "5")
	t.Cleanup(func() { _ = activityCmd.Flags().Set("lines", "50") })

	err := activityCmd.RunE(activityCmd, nil)
	if err == nil {
		t.Fatal("expected server error to bubble up")
	}
}
