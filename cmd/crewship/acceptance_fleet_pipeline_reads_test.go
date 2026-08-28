package main

// Acceptance for #2147's fleet/pipeline read slice, driven through the BUILT
// BINARY rather than by calling RunE in-process — the project's rule for an
// endpoint's CLI command (see acceptance_credential_openrouter_test.go's
// header). Five endpoints had no CLI command at all before this change:
//
//	GET /api/v1/workspaces/{id}/pipeline-runs                -> routine runs-all
//	GET /api/v1/workspaces/{id}/pipeline-runs/{runId}/changes -> routine changes
//	GET /api/v1/agents/crews-status                           -> agent status
//	GET /api/v1/agent-load                                    -> agent load
//	GET /api/v1/crewshipd                                     -> system crewshipd
//
// No network: each test points the binary at an httptest server via a config
// file, with the ambient CREWSHIP_* variables explicitly cleared so a box
// that exports CREWSHIP_SERVER cannot make a passing run mean something else.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fleetStubConfig writes a CLI config pointing at serverURL, matching
// credStubConfig's shape (server/workspace/token/format).
func fleetStubConfig(t *testing.T, serverURL string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runFleetCLI runs the built binary against the stub, with the ambient
// CREWSHIP_* variables scrubbed, and returns combined output + the exit
// error.
func runFleetCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fleetRoute is one {method, path} -> canned response the stub answers.
type fleetRoute struct {
	method, path string
	status       int // 0 means 200
	body         string
}

// newFleetStub starts an httptest server that answers exactly the given
// routes (recording the matched request's raw query into *gotQuery when
// non-nil) and 404s everything else — including, deliberately, the
// workspace-resolution preflight (GET /api/v1/workspaces) that "ws_test"
// (a non-CUID slug, like credStubConfig's) triggers on every request. An
// UNGATED stub that answers any path would also "answer" that preflight
// with the wrong body — silently steering workspace resolution onto a
// false WorkspaceNotFoundError depending on whether the wrong body happens
// to shape-match []{"id","slug"}. Gate on path like credStubServer does.
func newFleetStub(t *testing.T, gotQuery *string, routes ...fleetRoute) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, rt := range routes {
			if r.Method == rt.method && r.URL.Path == rt.path {
				if gotQuery != nil {
					*gotQuery = r.URL.RawQuery
				}
				w.Header().Set("Content-Type", "application/json")
				status := rt.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(rt.body))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ─── routine runs-all ───────────────────────────────────────────────────────

func TestAcceptance_RoutineRunsAll_HitsWorkspaceScopedListEndpoint(t *testing.T) {
	var gotQuery string
	srv := newFleetStub(t, &gotQuery, fleetRoute{
		method: "GET", path: "/api/v1/workspaces/ws_test/pipeline-runs",
		body: `{"count":1,"rows":[{"id":"run_abc","pipeline_id":"pln_x",` +
			`"pipeline_slug":"workspace-digest","pipeline_name":"Workspace digest",` +
			`"status":"completed","mode":"run","started_at":"2026-08-27T08:00:19Z",` +
			`"ended_at":"2026-08-27T08:00:20Z","current_step_id":"","cost_usd":0.01,` +
			`"duration_ms":1234,"triggered_via":"schedule","triggered_by_id":"psched_1",` +
			`"invoking_crew_id":"","invoking_agent_id":"","invoking_user_id":"",` +
			`"error_message":"","failed_at_step":"","issue_identifier":""}]}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "routine", "runs-all", "--status", "completed", "--since", "2026-08-01T00:00:00Z", "--limit", "10")
	if err != nil {
		t.Fatalf("routine runs-all: %v\noutput: %s", err, out)
	}
	if !strings.Contains(gotQuery, "status=completed") || !strings.Contains(gotQuery, "since=2026-08-01T00%3A00%3A00Z") || !strings.Contains(gotQuery, "limit=10") {
		t.Errorf("query %q did not carry all three filters", gotQuery)
	}
	if !strings.Contains(out, "workspace-digest") || !strings.Contains(out, "completed") {
		t.Errorf("table output missing the run's slug/status:\n%s", out)
	}
}

// The server wraps rows in {"rows":[...],"count":N}; the CLI's machine
// output must be the flat array (matching sibling list commands like
// 'routine active'), not the wrapper envelope re-exposed.
func TestAcceptance_RoutineRunsAll_JSONOutputIsFlatArray(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/workspaces/ws_test/pipeline-runs",
		body: `{"count":0,"rows":[]}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "routine", "runs-all", "--format", "json")
	if err != nil {
		t.Fatalf("routine runs-all: %v\noutput: %s", err, out)
	}
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil {
		t.Fatalf("stdout is not a JSON array (%v):\n%s", jsonErr, out)
	}
	if rows == nil {
		t.Errorf("decoded to a nil slice — an empty result must render `[]`, not `null`")
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty result rendered %q, want exactly []", strings.TrimSpace(out))
	}
}

// ─── routine changes ─────────────────────────────────────────────────────────

func TestAcceptance_RoutineChanges_ProxiesRunGitDiff(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/workspaces/ws_test/pipeline-runs/run_abc123/changes",
		body: `{"is_repo":true,"files":[{"path":"main.go","status":"modified",` +
			`"additions":3,"deletions":1}],"diff":"--- a/main.go\n+++ b/main.go\n","truncated":false}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "routine", "changes", "run_abc123")
	if err != nil {
		t.Fatalf("routine changes: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "modified") {
		t.Errorf("table output missing the changed file:\n%s", out)
	}
	if !strings.Contains(out, "--- a/main.go") {
		t.Errorf("output missing the unified diff body:\n%s", out)
	}
}

// The degrade path (no resolvable crew / not a git repo) is the NORMAL case
// for a workspace-level routine, not an error — it must exit 0 and say so
// in words, not dump raw JSON at a human.
func TestAcceptance_RoutineChanges_NotARepoDegradesCleanly(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/workspaces/ws_test/pipeline-runs/run_abc123/changes",
		body: `{"is_repo":false,"error":"git not available"}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "routine", "changes", "run_abc123")
	if err != nil {
		t.Fatalf("routine changes on a non-repo run should exit 0: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "git not available") {
		t.Errorf("degrade message dropped the server's reason:\n%s", out)
	}
}

// A run the server doesn't know about is a 404, and the CLI's structured
// error path (not a raw JSON dump) must carry it.
func TestAcceptance_RoutineChanges_UnknownRunIs404(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/workspaces/ws_test/pipeline-runs/run_nope/changes",
		status: http.StatusNotFound, body: `{"error":"Run not found"}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "routine", "changes", "run_nope")
	if err == nil {
		t.Fatalf("exited 0 for an unknown run:\n%s", out)
	}
	if code := exitCodeOf(t, err); code == 0 {
		t.Errorf("exit code 0 for a 404, want non-zero")
	}
}

// ─── agent status ────────────────────────────────────────────────────────────

func TestAcceptance_AgentStatus_HitsCrewsStatusEndpoint(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/agents/crews-status",
		body: `{"total":8,"running":1,"error":0,"idle":6,"queued":1}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "agent", "status")
	if err != nil {
		t.Fatalf("agent status: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"8", "1", "0", "6"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing count %q:\n%s", want, out)
		}
	}
}

// ─── agent load ──────────────────────────────────────────────────────────────

func TestAcceptance_AgentLoad_HitsAgentLoadEndpoint(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/agent-load",
		body: `[{"agent_id":"ag_1","agent_name":"Alex","agent_slug":"alex",` +
			`"agent_status":"RUNNING","active_tasks":2,"pending_tasks":1,"completed_today":5,` +
			`"tokens_used_today":1000,"token_budget":5000}]`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "agent", "load")
	if err != nil {
		t.Fatalf("agent load: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "alex") || !strings.Contains(out, "RUNNING") {
		t.Errorf("table output missing the agent row:\n%s", out)
	}
}

// A server returning the JSON literal `null` (the pre-guard shape of an
// idle workspace, per agents_query.go's own nil->[] fix) must still render
// `[]` on the CLI side, not `null` — the project-wide JSON-array contract.
func TestAcceptance_AgentLoad_NullServerBodyRendersEmptyArray(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/agent-load",
		body: `null`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "agent", "load", "--format", "json")
	if err != nil {
		t.Fatalf("agent load: %v\noutput: %s", err, out)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("null server body rendered %q, want exactly []", strings.TrimSpace(out))
	}
}

// ─── system crewshipd ────────────────────────────────────────────────────────

func TestAcceptance_SystemCrewshipd_ProxiesHealthEndpoint(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/crewshipd",
		body: `{"status":"ok","uptime":"1h2m3s","connections":4}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "system", "crewshipd", "--format", "json")
	if err != nil {
		t.Fatalf("system crewshipd: %v\noutput: %s", err, out)
	}
	var body map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &body); jsonErr != nil {
		t.Fatalf("stdout is not JSON (%v):\n%s", jsonErr, out)
	}
	if body["status"] != "ok" {
		t.Errorf("body[status] = %v, want \"ok\"", body["status"])
	}
}

// An unreachable sidecar answers 200 with {"status":"unreachable"} (see
// ProxyHandler.CrewshipdHealth) rather than an HTTP error — the CLI must
// pass that through rather than treating it as a request failure.
func TestAcceptance_SystemCrewshipd_UnreachableSidecarStaysExitZero(t *testing.T) {
	srv := newFleetStub(t, nil, fleetRoute{
		method: "GET", path: "/api/v1/crewshipd",
		body: `{"status":"unreachable"}`,
	})
	cfg := fleetStubConfig(t, srv.URL)

	out, err := runFleetCLI(t, cfg, "system", "crewshipd", "--format", "json")
	if err != nil {
		t.Fatalf("system crewshipd should pass through a 200 unreachable body, not fail: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "unreachable") {
		t.Errorf("output dropped the unreachable status:\n%s", out)
	}
}
