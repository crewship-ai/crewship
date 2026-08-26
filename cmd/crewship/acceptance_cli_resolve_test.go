package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance for #2086 — four commands that answer wrongly for reasons no
// in-process RunE test can see. Every case here drives the BUILT BINARY.
//
// That matters for exactly the things being fixed:
//
//   - `agent logs` decoded the wrong JSON shape, so the failure is in the
//     rendered error, not in a return value;
//   - `project get` / `skill proposed list` forward the raw argument into a
//     URL, so what must be asserted is the wire path the process produced;
//   - `memory status` returned nil after printing a failure, so the defect IS
//     the process exit status — a value RunE never gets to express.
//
// The stub below answers the way the real handlers answer TODAY (shapes copied
// from internal/api, not from the CLI's own decode structs) and, like the live
// server, 404s any id it does not recognise. A command that forwards a slug
// where an id belongs therefore fails here for the same reason it failed
// against crewship-dev3.

const (
	// CUID-shaped ids: 'c' + 20 lowercase alphanumerics, so looksLikeCUID
	// accepts them and the CUID fast paths are exercised for real.
	stubAgentCUID   = "cmta4hqit005b70f6a8e8"
	stubCrewCUID    = "cmta4hqir0058d8758e58"
	stubProjectCUID = "cmta4hqis005acbbf1f0f"
)

// resolveStubCall records one request the binary made.
type resolveStubCall struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// resolveStub answers the endpoints the four commands touch and records every
// request, so a test can assert on the URL the process actually built.
type resolveStub struct {
	mu    sync.Mutex
	calls []resolveStubCall
}

func (s *resolveStub) record(r *http.Request) map[string]any {
	var body map[string]any
	if r.Body != nil {
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			body = map[string]any{}
			_ = json.Unmarshal(raw, &body)
		}
	}
	s.mu.Lock()
	s.calls = append(s.calls, resolveStubCall{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
	})
	s.mu.Unlock()
	return body
}

// callsFor returns the recorded calls for a method+path pair.
func (s *resolveStub) callsFor(method, path string) []resolveStubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []resolveStubCall
	for _, c := range s.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (s *resolveStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	notFound := func(w http.ResponseWriter, what string) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"` + what + ` not found"}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := s.record(r)
		w.Header().Set("Content-Type", "application/json")

		switch {
		// ── agents ────────────────────────────────────────────────────
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents":
			_, _ = w.Write([]byte(`[{"id":"` + stubAgentCUID + `","slug":"viktor","name":"Viktor",` +
				`"crew_id":"` + stubCrewCUID + `","status":"RUNNING"}]`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/"+stubAgentCUID:
			_, _ = w.Write([]byte(`{"id":"` + stubAgentCUID + `","slug":"viktor","crew_id":"` + stubCrewCUID + `"}`))

		// proxy.AgentLogs (internal/api/proxy.go) writes the sidecar's
		// "logs" value straight out — an ARRAY of journal rows — or an
		// empty array. It has never written an object.
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/"+stubAgentCUID+"/logs":
			_, _ = w.Write([]byte(`[{"ts":"2026-08-26T13:33:53Z","level":"info","agent":"viktor",` +
				`"event":"output","content":"hello from the container"}]`))

		// ── crews ─────────────────────────────────────────────────────
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[{"id":"` + stubCrewCUID + `","slug":"ops","name":"Ops"}]`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews/"+stubCrewCUID:
			_, _ = w.Write([]byte(`{"id":"` + stubCrewCUID + `","slug":"ops","name":"Ops"}`))

		// ── projects ──────────────────────────────────────────────────
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
			_, _ = w.Write([]byte(`[{"id":"` + stubProjectCUID + `","name":"File Operations",` +
				`"slug":"file-operations","status":"in_progress","priority":"high","health":"on_track",` +
				`"issue_count":3,"done_count":0,"progress":0}]`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+stubProjectCUID:
			_, _ = w.Write([]byte(`{"id":"` + stubProjectCUID + `","name":"File Operations",` +
				`"slug":"file-operations","status":"in_progress","priority":"high","health":"on_track",` +
				`"issue_count":3,"done_count":0,"progress":0}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+stubProjectCUID+"/milestones":
			_, _ = w.Write([]byte(`[{"id":"mil_1","project_id":"` + stubProjectCUID + `","name":"Beta",` +
				`"status":"active","position":0,"issue_count":2,"done_count":1}]`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/"+stubProjectCUID+"/milestones":
			name, _ := body["name"].(string)
			_, _ = w.Write([]byte(`{"id":"mil_2","project_id":"` + stubProjectCUID + `","name":"` + name +
				`","status":"active","position":1,"issue_count":0,"done_count":0}`))

		// ── proposed skills ───────────────────────────────────────────
		// api.SkillsProposed resolves crew_id against the crews table, so
		// a slug is a miss — the same 404 the live server returned.
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/proposed":
			if r.URL.Query().Get("crew_id") != stubCrewCUID {
				notFound(w, "crew")
				return
			}
			_, _ = w.Write([]byte(`[{"file_name":"skill-deploy-friday.md","name":"Deploy Friday",` +
				`"description":"How the Friday deploy goes","description_quality":"ok","category":"ops"}]`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/proposed/approve":
			if body["crew_id"] != stubCrewCUID {
				notFound(w, "crew")
				return
			}
			_, _ = w.Write([]byte(`{"skill_id":"skl_1","slug":"deploy-friday","created":true,` +
				`"file_name":"skill-deploy-friday.md"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/skills/proposed/reject":
			if body["crew_id"] != stubCrewCUID {
				notFound(w, "crew")
				return
			}
			_, _ = w.Write([]byte(`{"file_name":"skill-noise.md","removed":true}`))

		default:
			notFound(w, "resource")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runResolveCLI runs the built binary against the stub. CREWSHIP_* is cleared
// so a shell that exports a dev server cannot make a passing run mean
// something else.
func runResolveCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ─── #2086 bug 1: `agent logs` decoded an object the handler never sends ───
//
// GET /api/v1/agents/{id}/logs writes an ARRAY. cmd_agent_introspect.go
// decoded into map[string]interface{}, so the command failed on EVERY agent
// with "json: cannot unmarshal array into Go value of type map[string]…".
// The top-level `crewship logs` reads the same route correctly, which is why
// the fix is one implementation rather than two.
func TestAcceptance_AgentLogs_ReadsTheArrayTheHandlerSends(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// wantLimit is the value the shared implementation must put on the
		// wire. proxy.AgentLogs reads `limit` via parsePagination; `tail`
		// is not a parameter it has ever looked at.
		wantLimit string
	}{
		{
			name:      "agent logs",
			args:      []string{"agent", "logs", "viktor"},
			wantLimit: "limit=100",
		},
		{
			name:      "agent logs --tail",
			args:      []string{"agent", "logs", "viktor", "--tail", "25"},
			wantLimit: "limit=25",
		},
		{
			// The path that already worked, kept in the table so the shared
			// implementation cannot be fixed for one caller and broken for
			// the other.
			name:      "logs",
			args:      []string{"logs", "viktor"},
			wantLimit: "limit=100",
		},
		{
			name:      "logs --lines",
			args:      []string{"logs", "viktor", "--lines", "25"},
			wantLimit: "limit=25",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resolveStub{}
			srv := stub.start(t)
			out, err := runResolveCLI(t, credStubConfig(t, srv.URL), tc.args...)
			if err != nil {
				t.Fatalf("%v exited %v\noutput: %s", tc.args, err, out)
			}
			if !strings.Contains(out, "hello from the container") {
				t.Errorf("log line missing from output:\n%s", out)
			}
			calls := stub.callsFor("GET", "/api/v1/agents/"+stubAgentCUID+"/logs")
			if len(calls) != 1 {
				t.Fatalf("logs calls = %d, want 1 (%v)", len(calls), calls)
			}
			if !strings.Contains(calls[0].Query, tc.wantLimit) {
				t.Errorf("query = %q, want it to carry %s — the handler reads `limit`, never `tail`",
					calls[0].Query, tc.wantLimit)
			}
		})
	}
}

// `agent logs --format json` must emit the entries as JSON rather than the
// coloured human lines. It could not before: the decode died first.
func TestAcceptance_AgentLogs_FormatJSONEmitsEntries(t *testing.T) {
	stub := &resolveStub{}
	srv := stub.start(t)
	out, err := runResolveCLI(t, credStubConfig(t, srv.URL), "agent", "logs", "viktor", "--format", "json")
	if err != nil {
		t.Fatalf("agent logs --format json exited %v\noutput: %s", err, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--format json output does not parse: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["content"] != "hello from the container" {
		t.Errorf("rows = %v", rows)
	}
}

// ─── #2086 bug 2: `project get <slug>` 404s on a slug `project list` printed ──
//
// Help said `get <id-or-slug>` and the docs' own example passes a slug, but
// the argument went straight into the URL. GET /api/v1/projects/{projectId}
// keys on the id, so every slug was a 404.
func TestAcceptance_ProjectCommands_AcceptTheSlugTheirHelpPromises(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantText string
		// wantPath is the URL the resolved id must produce.
		wantMethod, wantPath string
	}{
		{
			name:       "project get",
			args:       []string{"project", "get", "file-operations"},
			wantText:   "File Operations",
			wantMethod: "GET", wantPath: "/api/v1/projects/" + stubProjectCUID,
		},
		{
			name:       "project milestone list",
			args:       []string{"project", "milestone", "list", "file-operations"},
			wantText:   "Beta",
			wantMethod: "GET", wantPath: "/api/v1/projects/" + stubProjectCUID + "/milestones",
		},
		{
			name:       "project milestone create",
			args:       []string{"project", "milestone", "create", "file-operations", "--name", "GA"},
			wantText:   "GA",
			wantMethod: "POST", wantPath: "/api/v1/projects/" + stubProjectCUID + "/milestones",
		},
		// The id must keep working — a resolver that only understands slugs
		// would trade one broken half of the contract for the other.
		{
			name:       "project get by id",
			args:       []string{"project", "get", stubProjectCUID},
			wantText:   "File Operations",
			wantMethod: "GET", wantPath: "/api/v1/projects/" + stubProjectCUID,
		},
		{
			name:       "project milestone list by id",
			args:       []string{"project", "milestone", "list", stubProjectCUID},
			wantText:   "Beta",
			wantMethod: "GET", wantPath: "/api/v1/projects/" + stubProjectCUID + "/milestones",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resolveStub{}
			srv := stub.start(t)
			out, err := runResolveCLI(t, credStubConfig(t, srv.URL), tc.args...)
			if err != nil {
				t.Fatalf("%v exited %v\noutput: %s", tc.args, err, out)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output missing %q:\n%s", tc.wantText, out)
			}
			if got := stub.callsFor(tc.wantMethod, tc.wantPath); len(got) == 0 {
				stub.mu.Lock()
				all := stub.calls
				stub.mu.Unlock()
				t.Errorf("%s %s was never called; the CLI sent: %v", tc.wantMethod, tc.wantPath, all)
			}
		})
	}
}

// An unknown project reference is still a clean not-found, with the exit code
// the contract assigns to it — the resolver must not blur a typo into a
// generic failure.
func TestAcceptance_ProjectGet_UnknownRefIsExitNotFound(t *testing.T) {
	stub := &resolveStub{}
	srv := stub.start(t)
	out, err := runResolveCLI(t, credStubConfig(t, srv.URL), "project", "get", "no-such-project")
	if err == nil {
		t.Fatalf("expected a non-zero exit\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
	if !strings.Contains(out, "no-such-project") {
		t.Errorf("error does not name the reference the user typed:\n%s", out)
	}
}

// ─── #2086 bug 3: `skill proposed --crew` took an id where siblings take a slug ──
//
// `crew get`, `skill escalation`, `skill expose` and `integration crew list`
// all resolve a crew slug. These three did not, so the crew slug an operator
// had just read out of `crewship crew list` came back "crew not found".
func TestAcceptance_SkillProposed_AcceptsACrewSlug(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantText   string
		wantMethod string
		wantPath   string
	}{
		{
			name:       "proposed list --crew <slug>",
			args:       []string{"skill", "proposed", "list", "--crew", "ops"},
			wantText:   "skill-deploy-friday.md",
			wantMethod: "GET", wantPath: "/api/v1/skills/proposed",
		},
		{
			name:       "proposed approve --crew <slug>",
			args:       []string{"skill", "proposed", "approve", "--crew", "ops", "--file", "skill-deploy-friday.md"},
			wantText:   "approved skill-deploy-friday.md",
			wantMethod: "POST", wantPath: "/api/v1/skills/proposed/approve",
		},
		{
			name:       "proposed reject --crew <slug>",
			args:       []string{"skill", "proposed", "reject", "--crew", "ops", "--file", "skill-noise.md"},
			wantText:   "rejected skill-noise.md",
			wantMethod: "POST", wantPath: "/api/v1/skills/proposed/reject",
		},
		// The id form is the one that worked; it has to keep working.
		{
			name:       "proposed list --crew <id>",
			args:       []string{"skill", "proposed", "list", "--crew", stubCrewCUID},
			wantText:   "skill-deploy-friday.md",
			wantMethod: "GET", wantPath: "/api/v1/skills/proposed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := &resolveStub{}
			srv := stub.start(t)
			out, err := runResolveCLI(t, credStubConfig(t, srv.URL), tc.args...)
			if err != nil {
				t.Fatalf("%v exited %v\noutput: %s", tc.args, err, out)
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output missing %q:\n%s", tc.wantText, out)
			}
			calls := stub.callsFor(tc.wantMethod, tc.wantPath)
			if len(calls) != 1 {
				t.Fatalf("%s %s calls = %d, want 1", tc.wantMethod, tc.wantPath, len(calls))
			}
			// The resolved CUID, never the slug, goes on the wire.
			if tc.wantMethod == "GET" {
				if got := calls[0].Query; !strings.Contains(got, "crew_id="+stubCrewCUID) {
					t.Errorf("query = %q, want the resolved crew id", got)
				}
			} else if calls[0].Body["crew_id"] != stubCrewCUID {
				t.Errorf("crew_id = %v, want the resolved crew id", calls[0].Body["crew_id"])
			}
		})
	}
}

// An unknown crew is a client-side not-found with the contract's exit code,
// and it must not reach the server carrying a bad id.
func TestAcceptance_SkillProposed_UnknownCrewIsExitNotFound(t *testing.T) {
	stub := &resolveStub{}
	srv := stub.start(t)
	out, err := runResolveCLI(t, credStubConfig(t, srv.URL), "skill", "proposed", "list", "--crew", "no-such-crew")
	if err == nil {
		t.Fatalf("expected a non-zero exit\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
	}
	if got := stub.callsFor("GET", "/api/v1/skills/proposed"); len(got) != 0 {
		t.Errorf("an unresolvable crew still reached the server: %v", got)
	}
}

// ─── #2086 bug 4: `memory status` printed a driver error and exited 0 ────────
//
// Two defects in one line. `unable to open database file (14)` is SQLITE_
// CANTOPEN verbatim — it names no cause an operator can act on, and it is the
// same string for a missing directory, a file where a directory belongs, and a
// permission denial. And the loop `continue`d, so the command returned nil:
// every scripted `crewship memory status … || handle_it` was dead code.
func TestAcceptance_MemoryStatus_FailsLoudlyAndActionably(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-crew")

	notADir := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	tests := []struct {
		name string
		args []string
		// wantText is the actionable phrasing the operator must get.
		wantText string
	}{
		{
			name:     "missing directory",
			args:     []string{"memory", "status", "--path", missing},
			wantText: "does not exist",
		},
		{
			name:     "workspace scope on a missing directory",
			args:     []string{"memory", "status", "--path", missing, "--scope", "workspace"},
			wantText: "does not exist",
		},
		{
			name:     "a file where the memory directory belongs",
			args:     []string{"memory", "status", "--path", notADir, "--scope", "workspace"},
			wantText: "not a directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(buildCrewshipBinary(t), tc.args...)
			cmd.Env = offlineEnv(t)
			raw, err := cmd.CombinedOutput()
			out := string(raw)

			if err == nil {
				t.Fatalf("memory status exited 0 on a failure; no script can detect this\noutput: %s", out)
			}
			if got := exitCodeOf(t, err); got == 0 {
				t.Fatalf("exit code = 0 on a failure\noutput: %s", out)
			}
			// The raw driver string must not reach the user.
			for _, leak := range []string{"unable to open database file", "(14)", "init memory schema"} {
				if strings.Contains(out, leak) {
					t.Errorf("driver-level error %q leaked to the user:\n%s", leak, out)
				}
			}
			if !strings.Contains(out, tc.wantText) {
				t.Errorf("output does not say %q:\n%s", tc.wantText, out)
			}
			// Naming the path is what makes the message actionable — the
			// path is derived from --path plus --scope, so the user cannot
			// reconstruct it from the flags alone.
			if !strings.Contains(out, filepath.Base(tc.args[3])) {
				t.Errorf("output does not name the path it tried:\n%s", out)
			}
		})
	}
}

// …and a readable index still reports, so the fix is not "fail on everything".
func TestAcceptance_MemoryStatus_ReadableIndexStillReportsAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cmd := exec.Command(buildCrewshipBinary(t), "memory", "status", "--path", dir)
	cmd.Env = offlineEnv(t)
	raw, err := cmd.CombinedOutput()
	out := string(raw)
	if err != nil {
		t.Fatalf("memory status on a readable index exited %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Files:") || !strings.Contains(out, "Ready:") {
		t.Errorf("status report missing:\n%s", out)
	}
}
