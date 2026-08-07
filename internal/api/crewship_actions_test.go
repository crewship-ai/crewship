package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"

	_ "modernc.org/sqlite"
)

// crewshipPolicyDB builds the one table policy.Resolver reads, with a single
// crew at the requested autonomy level.
func crewshipPolicyDB(t *testing.T, crewID, level string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    autonomy_level TEXT,
    behavior_mode TEXT,
    autonomy_set_by_user_id TEXT,
    autonomy_set_at TEXT,
    autonomy_reason TEXT
);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, autonomy_level, behavior_mode) VALUES (?, ?, 'warn')`,
		crewID, level); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return db
}

// capturedCall is one request the fake internal API received.
type capturedCall struct {
	method string
	path   string
	token  string
	body   map[string]any
}

// fakeInternalAPI stands in for the daemon's own internal routes.
func fakeInternalAPI(t *testing.T, calls *[]capturedCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		*calls = append(*calls, capturedCall{
			method: r.Method,
			path:   r.URL.Path,
			token:  r.Header.Get("X-Internal-Token"),
			body:   body,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identifier":"ENG-42"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The master token buys no server-side workspace injection — the middleware's
// scope-pinning branches are for BOUND tokens only. So the dispatcher must
// inject identity itself, and an authored arg must never be able to replace
// it: a routine naming a foreign workspace_id or a sibling crew_id would be
// exactly the cross-tenant hole that lack of injection opens.
func TestCrewshipActions_InjectedIdentityBeatsAuthoredArgs(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_ok", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), slog.Default())
	if actions == nil {
		t.Fatal("dispatcher not constructed")
	}

	out, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "issue.create",
		Args: map[string]any{
			"title": "real title",
			// A routine author trying to act as another tenant.
			"workspace_id":    "ws_evil",
			"crew_id":         "crew_evil",
			"author_run_id":   "run_forged",
			"author_agent_id": "agent_forged",
		},
		WorkspaceID: "ws_real",
		CrewID:      "crew_ok",
		AgentID:     "agent_real",
		RunID:       "run_real",
		ChainDepth:  2,
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != `{"identifier":"ENG-42"}` {
		t.Errorf("output = %q, want the route's response body", out)
	}
	if len(calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(calls))
	}
	c := calls[0]
	if c.method != "POST" || c.path != "/api/v1/internal/issues" {
		t.Errorf("dispatched to %s %s", c.method, c.path)
	}
	if c.token != "master-token" {
		t.Errorf("X-Internal-Token = %q", c.token)
	}
	for field, want := range map[string]string{
		"workspace_id":    "ws_real",
		"crew_id":         "crew_ok",
		"author_agent_id": "agent_real",
		// The provenance that lets an automation reacting to this issue
		// resolve the originating run and inherit its chain budget.
		"author_run_id": "run_real",
	} {
		if got, _ := c.body[field].(string); got != want {
			t.Errorf("body[%q] = %q, want %q — the run's identity must win over the author's args", field, got, want)
		}
	}
	if got, _ := c.body["title"].(string); got != "real title" {
		t.Errorf("authored args must still pass through, got title=%q", got)
	}
}

// A crew that is not permitted to take the action unattended must not have it
// taken. `guided` holds mission_create for approval on the agent path; a
// routine has nobody to approve it, so a held decision is a refusal — and the
// message has to say what would unblock it, or the routine just "does
// nothing".
func TestCrewshipActions_HeldDecisionRefusesWithoutCalling(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_strict", "strict")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), slog.Default())
	_, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb:        "issue.create",
		Args:        map[string]any{"title": "x"},
		WorkspaceID: "ws_real",
		CrewID:      "crew_strict",
	})
	if err == nil {
		t.Fatal("a strict crew must not create an issue unattended")
	}
	if !strings.Contains(err.Error(), "autonomy_level=strict") {
		t.Errorf("the refusal must name the level that caused it, got %q", err)
	}
	if !strings.Contains(err.Error(), "crewship policy set") {
		t.Errorf("the refusal must name the fix, got %q", err)
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls despite the refusal — the gate must run BEFORE the write", len(calls))
	}
}

// An unwired gate is not an allow. A nil resolver means the policy layer was
// never wired, and gateInternalAction's literal fallback holds everything for
// exactly that reason; the routine path must inherit the same fail-closed.
func TestCrewshipActions_NilResolverFailsClosed(t *testing.T) {
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)

	actions := newCrewshipActions(srv.URL, "master-token", nil, slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: "issue.create", Args: map[string]any{"title": "x"},
		WorkspaceID: "ws", CrewID: "crew",
	}); err == nil {
		t.Fatal("an unwired policy resolver must fail closed, not wave the write through")
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls with no policy wired", len(calls))
	}
}

// A verb the registry declares but does not govern must not be dispatchable
// even if a definition smuggles it past save (an older build, a hand-edited
// row).
func TestCrewshipActions_UngovernedVerbRefusedAtDispatch(t *testing.T) {
	var ungoverned string
	for _, v := range pipeline.CrewshipVerbs() {
		if pipeline.CrewshipVerbPolicyAction(v) == "" {
			ungoverned = v
			break
		}
	}
	if ungoverned == "" {
		t.Skip("every crewship verb now has a policy action")
	}
	var calls []capturedCall
	srv := fakeInternalAPI(t, &calls)
	db := crewshipPolicyDB(t, "crew_full", "full")

	actions := newCrewshipActions(srv.URL, "master-token", policy.NewResolver(db), slog.Default())
	if _, err := actions.Do(context.Background(), pipeline.CrewshipRequest{
		Verb: ungoverned, Args: map[string]any{"identifier": "ENG-1", "body": "x"},
		WorkspaceID: "ws", CrewID: "crew_full",
	}); err == nil {
		t.Fatalf("%q has no policy action and must not dispatch", ungoverned)
	}
	if len(calls) != 0 {
		t.Errorf("made %d calls for an ungoverned verb", len(calls))
	}
}

// Route templates take their placeholder from the args, escaped — an
// identifier that renders empty must be an error, not `/issues//comments`,
// which 404s in a way nobody can read.
func TestCrewshipRoutePath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tmpl    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{"no placeholder", "/api/v1/internal/issues", nil, "/api/v1/internal/issues", false},
		{"filled", "/api/v1/internal/issues/{identifier}/comments",
			map[string]any{"identifier": "ENG-7"}, "/api/v1/internal/issues/ENG-7/comments", false},
		{"escaped", "/api/v1/internal/issues/{identifier}",
			map[string]any{"identifier": "a/b"}, "/api/v1/internal/issues/a%2Fb", false},
		{"missing", "/api/v1/internal/issues/{identifier}", map[string]any{}, "", true},
		{"blank after render", "/api/v1/internal/issues/{identifier}",
			map[string]any{"identifier": "  "}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := crewshipRoutePath(tc.tmpl, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
