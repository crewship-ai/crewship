package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

	// #2106: os.Stat needs only +x on the PARENT, so it succeeds on a
	// directory the caller cannot enter and reports IsDir() == true. The
	// fs.ErrPermission branch therefore never fired for the case its own
	// message names — a .memory dir written inside an agent container and
	// owned by uid 1001 — and SQLITE_CANTOPEN reached the user anyway.
	unreadable := t.TempDir()
	unreadableMem := filepath.Join(unreadable, ".memory")
	if err := os.MkdirAll(unreadableMem, 0o700); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.Chmod(unreadableMem, 0o000); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	// Restore before TempDir's RemoveAll runs, or the cleanup fails the test
	// for a reason that has nothing to do with what is being asserted.
	t.Cleanup(func() { _ = os.Chmod(unreadableMem, 0o700) })

	tests := []struct {
		name string
		args []string
		// path is what --path pointed at, which the message must name: the
		// directory actually opened is --path plus --scope, so the user
		// cannot reconstruct it from the flags alone.
		path string
		// wantText is the actionable phrasing the operator must get.
		wantText string
		// skipAsRoot marks a case whose fixture is a permission bit. root
		// bypasses those, so the case would assert the opposite of the truth.
		skipAsRoot bool
	}{
		{
			name:     "missing directory",
			args:     []string{"memory", "status", "--path", missing},
			path:     missing,
			wantText: "does not exist",
		},
		{
			name:     "workspace scope on a missing directory",
			args:     []string{"memory", "status", "--path", missing, "--scope", "workspace"},
			path:     missing,
			wantText: "does not exist",
		},
		{
			name:     "a file where the memory directory belongs",
			args:     []string{"memory", "status", "--path", notADir, "--scope", "workspace"},
			path:     notADir,
			wantText: "not a directory",
		},
		{
			name:       "a memory directory the caller cannot enter",
			args:       []string{"memory", "status", "--path", unreadable},
			path:       unreadableMem,
			wantText:   "permission denied",
			skipAsRoot: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipAsRoot && os.Geteuid() == 0 {
				// SKIP-WAIVER: a directory the caller cannot enter IS the
				// premise, and root enters a 0o000 directory anyway — at
				// euid 0 the case would report ok while asserting the
				// opposite of what it claims. There is no permission-free
				// route to EACCES: ENOTDIR and ELOOP, which no euid
				// bypasses, land in the other branches of memoryDirError
				// and are what the "not a directory" case covers. Permanent
				// platform guard, not debt, so deliberately no tracking
				// issue — the #1546 precedent in scripts/skip-budget.txt.
				// The other three cases in this table run as root.
				t.Skip("running as root: directory permissions are not enforced")
			}
			cmd := exec.Command(buildCrewshipBinary(t), tc.args...)
			cmd.Env = offlineEnv(t)
			raw, err := cmd.CombinedOutput()
			out := string(raw)

			if err == nil {
				t.Fatalf("memory status exited 0 on a failure; no script can detect this\noutput: %s", out)
			}
			if got := exitCodeOf(t, err); got != cli.ExitNotFound {
				t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
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
			if !strings.Contains(out, tc.path) {
				t.Errorf("output does not name the path it tried (%s):\n%s", tc.path, out)
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

// ─── #2106 follow-up: the agent lookup behind both log commands saw one page ──
//
// GET /api/v1/agents is paginated: parseListPagination(r, 100, 500)
// (internal/api/agents.go), and the list query ends in LIMIT ? OFFSET ?. A
// scan that sends no `limit` therefore sees the first 100 rows and nothing
// else. #2086 consolidated `agent logs` onto that scan, and in doing so
// dropped resolveAgentID's by-id fast path — a direct GET
// /api/v1/agents/{id} with no such ceiling. Past 100 agents both log commands
// answered "agent not found" for an agent that exists, and `agent logs <cuid>`,
// which had worked at any roster size, regressed with them.

// pagedAgentStub serves a roster of agents the way the real List handler does
// — `limit` defaulting to 100 and capped at 500, `offset` skipping — so a
// caller that forgets the parameter gets exactly the truncation production
// gives it.
type pagedAgentStub struct {
	mu    sync.Mutex
	calls []resolveStubCall
	ids   []string
	slugs []string
}

// newPagedAgentStub builds a roster of n agents named agent-0…agent-(n-1),
// each with a CUID-shaped id so looksLikeCUID accepts it and the by-id fast
// path is exercised for real rather than simulated.
func newPagedAgentStub(n int) *pagedAgentStub {
	s := &pagedAgentStub{}
	for i := 0; i < n; i++ {
		// 'c' + 20 lowercase alphanumerics.
		s.ids = append(s.ids, "c"+strings.Repeat("0", 20-len(strconv.Itoa(i)))+strconv.Itoa(i))
		s.slugs = append(s.slugs, "agent-"+strconv.Itoa(i))
	}
	return s
}

func (s *pagedAgentStub) callsFor(method, path string) []resolveStubCall {
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

func (s *pagedAgentStub) row(i int) string {
	return `{"id":"` + s.ids[i] + `","slug":"` + s.slugs[i] + `","crew_id":"` + stubCrewCUID + `"}`
}

func (s *pagedAgentStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls = append(s.calls, resolveStubCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if limit <= 0 {
				limit = 100
			}
			if limit > 500 {
				limit = 500
			}
			if offset < 0 {
				offset = 0
			}
			rows := make([]string, 0, limit)
			for i := offset; i < len(s.ids) && len(rows) < limit; i++ {
				rows = append(rows, s.row(i))
			}
			_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
			return
		}
		for i, id := range s.ids {
			if r.URL.Path == "/api/v1/agents/"+id {
				_, _ = w.Write([]byte(s.row(i)))
				return
			}
			if r.URL.Path == "/api/v1/agents/"+id+"/logs" {
				_, _ = w.Write([]byte(`[{"ts":"2026-08-26T13:33:53Z","level":"info","agent":"` +
					s.slugs[i] + `","event":"output","content":"hello from the container"}]`))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"resource not found"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAcceptance_AgentLogs_ResolvesPastTheFirstPageOfAgents(t *testing.T) {
	const roster = 150
	const target = 120 // past the handler's default LIMIT 100

	tests := []struct {
		name string
		// ref is filled in from the roster at run time.
		useCUID bool
		args    func(ref string) []string
		// wantListCalls is how many times the LIST endpoint may be read.
		// A CUID must not need it at all: that direct GET is the only
		// lookup with no page ceiling, which is the point of the fast path.
		wantListCalls int
	}{
		{
			name:          "logs <slug> past the first page",
			args:          func(ref string) []string { return []string{"logs", ref} },
			wantListCalls: 1,
		},
		{
			name:          "agent logs <slug> past the first page",
			args:          func(ref string) []string { return []string{"agent", "logs", ref} },
			wantListCalls: 1,
		},
		{
			name:          "agent logs <cuid> takes the uncapped by-id path",
			useCUID:       true,
			args:          func(ref string) []string { return []string{"agent", "logs", ref} },
			wantListCalls: 0,
		},
		{
			name:          "logs <cuid> takes the uncapped by-id path",
			useCUID:       true,
			args:          func(ref string) []string { return []string{"logs", ref} },
			wantListCalls: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newPagedAgentStub(roster)
			srv := stub.start(t)
			ref := stub.slugs[target]
			if tc.useCUID {
				ref = stub.ids[target]
			}
			out, err := runResolveCLI(t, credStubConfig(t, srv.URL), tc.args(ref)...)
			if err != nil {
				t.Fatalf("%v exited %v — agent %d of %d is not findable\noutput: %s",
					tc.args(ref), err, target, roster, out)
			}
			if !strings.Contains(out, "hello from the container") {
				t.Errorf("log line missing from output:\n%s", out)
			}
			if got := len(stub.callsFor("GET", "/api/v1/agents")); got != tc.wantListCalls {
				t.Errorf("LIST reads = %d, want %d", got, tc.wantListCalls)
			}
			if got := stub.callsFor("GET", "/api/v1/agents/"+stub.ids[target]+"/logs"); len(got) != 1 {
				t.Errorf("logs calls = %d, want 1", len(got))
			}
		})
	}
}

// A typo past fuzzy.Nearest's threshold (len(target)/3, min 2) gets no
// suggestions, and every sibling command falls back to listing what IS there.
// The log commands used to stop at a bare "agent not found", so the same typo
// got two different levels of help depending on which command you typed.
func TestAcceptance_AgentLogs_UnknownSlugListsWhatIsAvailable(t *testing.T) {
	for _, args := range [][]string{
		{"logs", "zzz"},
		{"agent", "logs", "zzz"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stub := newPagedAgentStub(3)
			srv := stub.start(t)
			out, err := runResolveCLI(t, credStubConfig(t, srv.URL), args...)
			if err == nil {
				t.Fatalf("expected a non-zero exit\noutput: %s", out)
			}
			if got := exitCodeOf(t, err); got != cli.ExitNotFound {
				t.Errorf("exit code = %d, want ExitNotFound (%d)\noutput: %s", got, cli.ExitNotFound, out)
			}
			if !strings.Contains(out, "Available:") || !strings.Contains(out, "agent-0") {
				t.Errorf("a typo past the suggestion threshold must still list what exists, got:\n%s", out)
			}
		})
	}
}

// ─── #2106 follow-up: the advice `status` gives has to work ─────────────────
//
// The "does not exist" message ends "…then build an index with `crewship
// memory reindex`", and reindex applied the very same gate before calling
// memory.New — which never mkdirs, because sql.Open is lazy and SQLite will
// not create a directory. Following the advice reproduced the identical
// error, one exit code lower. reindex is the command that BUILDS an index, so
// it now creates the directory it was asked to build in.
func TestAcceptance_MemoryReindex_FollowsTheAdviceStatusGives(t *testing.T) {
	// A real crew directory that has never been indexed: the base path is
	// there, .memory is not. This is the case an operator actually hits.
	base := t.TempDir()

	statusOut, err := runMemoryCLI(t, "memory", "status", "--path", base)
	if err == nil {
		t.Fatalf("status on an unindexed crew dir exited 0\noutput: %s", statusOut)
	}
	if !strings.Contains(statusOut, "crewship memory reindex") {
		t.Fatalf("status no longer advises reindex; this test is testing the wrong thing:\n%s", statusOut)
	}

	// Do exactly what it said.
	reindexOut, err := runMemoryCLI(t, "memory", "reindex", "--path", base)
	if err != nil {
		t.Fatalf("the advice loops: reindex exited %v on the path status sent it to\noutput: %s", err, reindexOut)
	}
	if _, statErr := os.Stat(filepath.Join(base, ".memory")); statErr != nil {
		t.Fatalf("reindex reported success without creating the index directory: %v", statErr)
	}

	// …and the command that sent us here now answers.
	statusOut, err = runMemoryCLI(t, "memory", "status", "--path", base)
	if err != nil {
		t.Fatalf("status still fails after following its own advice: %v\noutput: %s", err, statusOut)
	}
	if !strings.Contains(statusOut, "Files:") {
		t.Errorf("status report missing:\n%s", statusOut)
	}
}

// The third door into the same loop: a base path that EXISTS but is not
// writable. createMemoryDir's mkdir fails there, and while its error was
// dropped the gate behind it saw only "the directory is not there" — so
// reindex answered with the message that advises running `crewship memory
// reindex`, which is the command that had just failed to create it. Circular,
// and false: the message claims "which creates it".
func TestAcceptance_MemoryReindex_SaysWhyItCannotCreateTheIndexDir(t *testing.T) {
	if os.Geteuid() == 0 {
		// SKIP-WAIVER: an unwritable parent IS the premise, and root writes
		// into a 0o500 directory regardless — at euid 0 the mkdir succeeds
		// and the case would assert the opposite of what it claims. Same
		// permanent platform guard as the unreadable-.memory case in
		// TestAcceptance_MemoryStatus_FailsLoudlyAndActionably, and
		// deliberately no tracking issue, per the #1546 precedent in
		// scripts/skip-budget.txt.
		t.Skip("running as root: directory permissions are not enforced")
	}

	base := filepath.Join(t.TempDir(), "ro-crew")
	if err := os.Mkdir(base, 0o500); err != nil { // readable + searchable, not writable
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })

	out, err := runMemoryCLI(t, "memory", "reindex", "--path", base)
	if err == nil {
		t.Fatalf("reindex exited 0 with no index built\noutput: %s", out)
	}

	// The failure has to name the cause. "does not exist" is the message
	// that sends the operator back to this same command.
	if strings.Contains(out, "does not exist") {
		t.Errorf("an unwritable parent was reported as a missing directory, which advises re-running reindex:\n%s", out)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("output does not say why the directory could not be created:\n%s", out)
	}
	// The parent is the thing to fix, so the parent is what must be named.
	if !strings.Contains(out, base) {
		t.Errorf("output does not name the unwritable parent (%s):\n%s", base, out)
	}
	for _, leak := range []string{"unable to open database file", "(14)", "init memory schema"} {
		if strings.Contains(out, leak) {
			t.Errorf("driver-level error %q leaked to the user:\n%s", leak, out)
		}
	}
}

// The other half of that: reindex creating a directory must not turn a typo
// into a new empty tree. The base path itself is the thing --path names, and
// when IT is missing the answer is still "check --path", not mkdir -p.
func TestAcceptance_MemoryReindex_DoesNotInventAMissingBasePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "typo-crew")

	out, err := runMemoryCLI(t, "memory", "reindex", "--path", missing)
	if err == nil {
		t.Fatalf("reindex invented a base path the user mistyped\noutput: %s", out)
	}
	if got := exitCodeOf(t, err); got != cli.ExitNotFound {
		t.Errorf("exit code = %d, want ExitNotFound (%d) — the same condition exits 3 from `status`\noutput: %s",
			got, cli.ExitNotFound, out)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Errorf("%s was created", missing)
	}
	for _, leak := range []string{"unable to open database file", "(14)", "init memory schema"} {
		if strings.Contains(out, leak) {
			t.Errorf("driver-level error %q leaked to the user:\n%s", leak, out)
		}
	}
}

// runMemoryCLI drives the built binary offline — the memory commands are
// filesystem-only and must not need a server.
func runMemoryCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = offlineEnv(t)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ─── #2106: the crew lookup had the agent lookup's ceiling too ──────────────
//
// GET /api/v1/crews paginates exactly like the agent list —
// parseListPagination(r, 100, 500) in internal/api/crews_query.go, and the
// query ends in LIMIT ? OFFSET ?. resolveCrewID sent no `limit`, so it saw the
// first 100 crews and answered "crew not found" for one that exists past them.
//
// That was already true before this branch, but the branch is what makes it
// reachable: `skill proposed list|approve|reject --crew` used to demand the
// CUID, which never touches the scan, and now takes the slug its help
// advertises. Fixing the agent ceiling and leaving this one would have left
// the identical defect one command away.

// pagedCrewStub serves a roster of crews the way the real List handler does,
// so a caller that forgets `limit` gets exactly the truncation production
// gives it. Crews are the only thing it pages; /api/v1/skills/proposed answers
// on the resolved id, the same as the live route, which resolves crew_id
// against the crews table and 404s a slug.
type pagedCrewStub struct {
	mu    sync.Mutex
	calls []resolveStubCall
	ids   []string
	slugs []string
}

func newPagedCrewStub(n int) *pagedCrewStub {
	s := &pagedCrewStub{}
	for i := 0; i < n; i++ {
		s.ids = append(s.ids, "c"+strings.Repeat("0", 20-len(strconv.Itoa(i)))+strconv.Itoa(i))
		s.slugs = append(s.slugs, "crew-"+strconv.Itoa(i))
	}
	return s
}

func (s *pagedCrewStub) callsFor(method, path string) []resolveStubCall {
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

func (s *pagedCrewStub) row(i int) string {
	return `{"id":"` + s.ids[i] + `","slug":"` + s.slugs[i] + `","name":"Crew ` + strconv.Itoa(i) + `"}`
}

func (s *pagedCrewStub) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews" {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			if limit <= 0 {
				limit = 100
			}
			if limit > 500 {
				limit = 500
			}
			if offset < 0 {
				offset = 0
			}
			rows := make([]string, 0, limit)
			for i := offset; i < len(s.ids) && len(rows) < limit; i++ {
				rows = append(rows, s.row(i))
			}
			_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
			return
		}
		for i, id := range s.ids {
			if r.URL.Path == "/api/v1/crews/"+id {
				_, _ = w.Write([]byte(s.row(i)))
				return
			}
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/skills/proposed" {
			if !s.knownID(r.URL.Query().Get("crew_id")) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"crew not found"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"file_name":"skill-deploy-friday.md","name":"Deploy Friday",` +
				`"description":"How the Friday deploy goes","description_quality":"ok","category":"ops"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"resource not found"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *pagedCrewStub) knownID(id string) bool {
	for _, known := range s.ids {
		if known == id {
			return true
		}
	}
	return false
}

func TestAcceptance_SkillProposed_ResolvesPastTheFirstPageOfCrews(t *testing.T) {
	const roster = 150
	const target = 120 // past the handler's default LIMIT 100

	tests := []struct {
		name    string
		useCUID bool
		// wantListCalls is how many times the LIST may be read. A CUID must
		// not need it at all — /api/v1/crews/{id} is the lookup with no
		// ceiling, and asserting zero is what stops this passing on a fix
		// that merely raised the limit.
		wantListCalls int
	}{
		{name: "slug past the first page", wantListCalls: 1},
		{name: "cuid takes the uncapped by-id path", useCUID: true, wantListCalls: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newPagedCrewStub(roster)
			srv := stub.start(t)
			ref := stub.slugs[target]
			if tc.useCUID {
				ref = stub.ids[target]
			}

			out, err := runResolveCLI(t, credStubConfig(t, srv.URL),
				"skill", "proposed", "list", "--crew", ref)
			if err != nil {
				t.Fatalf("skill proposed list --crew %s exited %v — crew %d of %d is not findable\noutput: %s",
					ref, err, target, roster, out)
			}
			if !strings.Contains(out, "skill-deploy-friday.md") {
				t.Errorf("proposal row missing from output:\n%s", out)
			}
			if got := len(stub.callsFor("GET", "/api/v1/crews")); got != tc.wantListCalls {
				t.Errorf("LIST reads = %d, want %d", got, tc.wantListCalls)
			}

			// The id on the wire has to be the resolved CUID, not the ref
			// the operator typed — the route resolves crew_id against the
			// crews table and 404s a slug.
			calls := stub.callsFor("GET", "/api/v1/skills/proposed")
			if len(calls) != 1 {
				t.Fatalf("proposed reads = %d, want 1", len(calls))
			}
			if want := "crew_id=" + stub.ids[target]; !strings.Contains(calls[0].Query, want) {
				t.Errorf("query = %q, want it to carry %q", calls[0].Query, want)
			}
		})
	}
}
