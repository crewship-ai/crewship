package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// E0 run identity — docs/prd/WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md
// §4, IMPLEMENTATION §7, tests T07 ("Start/cleanup/cancel B during A ... A
// pokračuje").
//
// The defect these cover: every runtime name an agent run owned was derived
// from the agent SLUG, so two runs of one agent shared all of them —
//
//	tmux session   agent-<slug>
//	args file      /tmp/agent-<slug>.args   (B overwrote A's argv)
//	env file       /tmp/agent-<slug>.env    (B overwrote A's exported creds)
//	inner script   /tmp/agent-<slug>.sh
//	FIFO           /tmp/agent-<slug>.fifo
//	exit file      /tmp/agent-<slug>.exit   (A read B's exit code)
//	wait-for       agent-<slug>-done
//
// — and setupTmuxExec opens with an unconditional `tmux kill-session -t
// <that name>`, so starting B killed A outright.
// ---------------------------------------------------------------------------

// ---- ValidRunID ----

func TestValidRunID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// The shapes real dispatch paths actually mint. These are literal run
		// IDs, not credentials — the CUID's entropy is what trips the generic
		// key rule, and a run id is a public identifier that appears in the
		// journal and in URLs.
		{"api generateCUID", "c0k3xj9q0001abcd1234ef56", true}, //gitleaks:allow
		{"scheduler generateID", "sched_1757000000000000000_0011aabbccddeeff00112233", true},
		{"chatbridge generateMsgID", "msg_1757000000000000000_0011aabbccddeeff", true},
		{"orchestrator NewRunID", "run_1757000000000000000_0011aabbccddeeff", true},
		{"plain hyphens", "run-a", true},

		{"empty", "", false},
		{"path traversal", "../escape", false},
		{"path separator", "a/b", false},
		{"tmux window separator", "run.1", false},
		{"tmux pane separator", "run:1", false},
		{"space", "run a", false},
		{"single quote breaks the sh -c quoting", "run'x", false},
		{"command substitution", "run$(whoami)", false},
		{"backticks", "run`id`", false},
		{"semicolon", "run;rm -rf /", false},
		{"newline", "run\nrm", false},
		{"over the length cap", strings.Repeat("a", 81), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRunID(tc.in); got != tc.want {
				t.Errorf("ValidRunID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewRunID_IsUniqueAndValid(t *testing.T) {
	seen := make(map[string]bool, 512)
	for i := 0; i < 512; i++ {
		id := NewRunID()
		if !ValidRunID(id) {
			t.Fatalf("NewRunID() produced %q, which ValidRunID rejects", id)
		}
		if seen[id] {
			t.Fatalf("NewRunID() collided on %q", id)
		}
		seen[id] = true
	}
}

// ---- ensureRunID ----

func TestEnsureRunID(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cases := []struct {
		name    string
		in      string
		wantErr bool
		// wantSame: the id must survive untouched (never rewritten — the
		// journal, the idempotency table and the run row all carry it).
		wantSame bool
	}{
		{"a real minted id passes through", "c0k3xj9q0001abcd1234ef56", false, true},
		{"empty is filled in", "", false, false},
		{"an unsafe id is a hard error, not a silent replacement", "../escape", true, false},
		{"a quote is a hard error", "run'x", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := AgentRunRequest{AgentSlug: "eva", RunID: tc.in}
			err := o.ensureRunID(&req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ensureRunID(%q) = nil, want an error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureRunID(%q): %v", tc.in, err)
			}
			if !ValidRunID(req.RunID) {
				t.Errorf("ensureRunID left an id ValidRunID rejects: %q", req.RunID)
			}
			if tc.wantSame && req.RunID != tc.in {
				t.Errorf("ensureRunID rewrote a valid id: %q → %q", tc.in, req.RunID)
			}
			if !tc.wantSame && tc.in != "" && req.RunID == tc.in {
				t.Errorf("expected %q to be replaced", tc.in)
			}
		})
	}
}

// ---- RunIDsFromSessionNames ----

func TestRunIDsFromSessionNames(t *testing.T) {
	const out = `agent-eva-run-a
agent-eva-run-b
agent-eva2-run-c
agent-sam-run-d
agent-eva-bad id
agent-eva-../escape

other-session
`
	cases := []struct {
		name string
		slug string
		want []string
	}{
		{
			// Both of eva's runs, and NOTHING belonging to eva2 — the
			// trailing "-" in TmuxSessionPrefix is what keeps those apart.
			name: "both runs of one agent, no neighbours",
			slug: "eva",
			want: []string{"run-a", "run-b"},
		},
		{"the neighbour whose slug is a prefix", "eva2", []string{"run-c"}},
		{"an agent with one run", "sam", []string{"run-d"}},
		{"an agent with none", "nobody", nil},
		{"no slug at all resolves to nothing", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RunIDsFromSessionNames(out, tc.slug)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("RunIDsFromSessionNames(_, %q) = %v, want %v", tc.slug, got, tc.want)
			}
		})
	}
}

// ---- setupTmuxExec: every derived path is run-scoped ----

// recordingExecContainer is mockContainer with a thread-safe record of every
// command it was asked to run, which is where the actual generated file names
// show up: setupTmuxExec writes them via one batched `sh -c` and names them
// again in the wrapper it returns.
type recordingExecContainer struct {
	mockContainer
	mu   sync.Mutex
	cmds []string
}

func (r *recordingExecContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, strings.Join(cfg.Cmd, " "))
	r.mu.Unlock()
	return &provider.ExecResult{ExecID: "exec-rec", Reader: io.NopCloser(strings.NewReader(""))}, nil
}

func (r *recordingExecContainer) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.cmds))
	copy(out, r.cmds)
	return out
}

func newTmuxExecOrchestrator() (*Orchestrator, *recordingExecContainer) {
	rc := &recordingExecContainer{}
	// `command -v tmux` must report success (running=false, exit 0) or
	// setupTmuxExec bails before deriving anything.
	rc.inspectResult.running = false
	rc.inspectResult.exitCode = 0
	return New(rc, newMemState(), slog.Default()), rc
}

// derivedPaths is what setupTmuxExec is contractually required to scope by
// run. Held as a table so a seventh derived name added later without a
// matching run scope shows up as a missing row here rather than as a silent
// cross-run collision in production.
func derivedPaths(slug, runID string) map[string]string {
	session := TmuxSessionName(slug, runID)
	return map[string]string{
		"tmux session": session,
		"args file":    "/tmp/" + session + ".args",
		"env file":     "/tmp/" + session + ".env",
		"inner script": "/tmp/" + session + ".sh",
		"FIFO":         "/tmp/" + session + ".fifo",
		"exit file":    "/tmp/" + session + ".exit",
		"wait-for":     session + "-done",
	}
}

func TestSetupTmuxExec_EveryDerivedPathIsRunScoped(t *testing.T) {
	o, rc := newTmuxExecOrchestrator()
	wrapper, err := o.setupTmuxExec(context.Background(), "c1",
		[]string{"claude", "-p", "hello"}, "eva", "run-a", []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("setupTmuxExec: %v", err)
	}
	// The generated strings, from the two places they actually appear: the
	// batched write exec, and the returned wrapper. NOT re-derived by calling
	// the helper and comparing it to itself.
	generated := strings.Join(append(rc.recorded(), strings.Join(wrapper, " ")), "\n")

	for name, want := range derivedPaths("eva", "run-a") {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(generated, want) {
				t.Errorf("%s: expected %q in the generated commands, got:\n%s", name, want, generated)
			}
			// And the pre-E0 slug-only form must be gone. Checked as a whole
			// token so "agent-eva-run-a" does not satisfy a search for
			// "agent-eva".
			for _, stale := range []string{
				"/tmp/agent-eva.args", "/tmp/agent-eva.env", "/tmp/agent-eva.sh",
				"/tmp/agent-eva.fifo", "/tmp/agent-eva.exit", "'agent-eva'", "'agent-eva-done'",
			} {
				if strings.Contains(generated, stale) {
					t.Errorf("slug-only name %q is still generated:\n%s", stale, generated)
				}
			}
		})
	}
}

// TestSetupTmuxExec_TwoRunsShareNothing is the property in one assertion: for
// two runs of the SAME agent, no derived name is equal.
func TestSetupTmuxExec_TwoRunsShareNothing(t *testing.T) {
	a := derivedPaths("eva", "run-a")
	b := derivedPaths("eva", "run-b")
	for name, av := range a {
		if bv := b[name]; av == bv {
			t.Errorf("%s is shared by two runs of one agent: %q", name, av)
		}
	}
	// One run, asked twice, is stable — otherwise the wrapper and the write
	// exec would disagree about which file to read.
	if again := derivedPaths("eva", "run-a"); again["exit file"] != a["exit file"] {
		t.Errorf("exit file is not stable for one run: %q vs %q", a["exit file"], again["exit file"])
	}
}

// TestSetupTmuxExec_StartingBLeavesARunning is THE regression (T07).
//
// Run A's setup happens, then run B's. B must not name a single one of A's
// files or A's session — in particular its opening `tmux kill-session` (still
// unconditional, and still the first thing the wrapper does) must target B's
// own session. Before E0 that one command ended A's process tree.
func TestSetupTmuxExec_StartingBLeavesARunning(t *testing.T) {
	o, rc := newTmuxExecOrchestrator()

	wrapperA, err := o.setupTmuxExec(context.Background(), "c1",
		[]string{"claude", "-p", "A's prompt"}, "eva", "run-a", []string{"ANTHROPIC_API_KEY=key-A"})
	if err != nil {
		t.Fatalf("setup A: %v", err)
	}
	nA := len(rc.recorded())

	wrapperB, err := o.setupTmuxExec(context.Background(), "c1",
		[]string{"claude", "-p", "B's prompt"}, "eva", "run-b", []string{"ANTHROPIC_API_KEY=key-B"})
	if err != nil {
		t.Fatalf("setup B: %v", err)
	}
	// Everything B did: its own setup execs plus the wrapper it will run.
	fromB := strings.Join(append(rc.recorded()[nA:], strings.Join(wrapperB, " ")), "\n")

	for name, aPath := range derivedPaths("eva", "run-a") {
		if strings.Contains(fromB, aPath) {
			t.Errorf("starting run B names run A's %s (%q):\n%s", name, aPath, fromB)
		}
	}

	// The kill-session is still there — item 3 of the E0 cut keeps it and
	// makes it harmless rather than removing it — and it targets B.
	joinedB := strings.Join(wrapperB, " ")
	if !strings.Contains(joinedB, "tmux kill-session -t '"+TmuxSessionName("eva", "run-b")+"'") {
		t.Errorf("run B's wrapper does not kill its OWN session first:\n%s", joinedB)
	}
	if strings.Contains(joinedB, "kill-session -t '"+TmuxSessionName("eva", "run-a")+"'") {
		t.Errorf("run B's wrapper kills run A's session:\n%s", joinedB)
	}
	// And A's own wrapper still reads A's argv and A's exit code.
	joinedA := strings.Join(wrapperA, " ")
	if !strings.Contains(joinedA, "/tmp/"+TmuxSessionName("eva", "run-a")+".exit") {
		t.Errorf("run A's wrapper lost its own exit file:\n%s", joinedA)
	}
}

func TestSetupTmuxExec_RejectsUnsafeRunID(t *testing.T) {
	o, _ := newTmuxExecOrchestrator()
	for _, bad := range []string{"", "../escape", "run'x", "run;id"} {
		if _, err := o.setupTmuxExec(context.Background(), "c1",
			[]string{"claude"}, "eva", bad, nil); err == nil {
			t.Errorf("setupTmuxExec accepted run id %q — it is interpolated into an sh -c script", bad)
		}
	}
}

// ---- RunAgent boundary: every dispatch reaches the orchestrator with a run id ----

// TestRunAgent_AlwaysHasARunID is the boundary assertion the brief asks for,
// and it is worth more than one test per dispatch site: whatever a caller
// passes, by the time anything derives a runtime path the request carries a
// valid, non-empty run id — and a caller's own id is never rewritten.
func TestRunAgent_AlwaysHasARunID(t *testing.T) {
	cases := []struct {
		name  string
		runID string
		// wantKey is the state key the run row must land under; "" means
		// "whatever was minted", checked by scanning the bucket instead.
		wantKey string
	}{
		{"caller passed its own id", "c0k3xj9q0001abcd1234ef56", "c0k3xj9q0001abcd1234ef56"},
		{"caller passed none", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mc := &mockContainer{
				execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
					return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(""))}, nil
				},
			}
			state := newMemState()
			o := New(mc, state, slog.Default())
			if err := o.RunAgent(context.Background(), AgentRunRequest{
				AgentID:     "a1",
				AgentSlug:   "eva",
				ChatID:      "chat-shared",
				RunID:       tc.runID,
				ContainerID: "c1",
				CLIAdapter:  "CLAUDE_CODE",
				UserMessage: "hi",
				TimeoutSecs: 30,
			}, nil); err != nil {
				t.Fatalf("RunAgent: %v", err)
			}

			rows, _ := state.List(context.Background(), "agent_runs")
			if len(rows) != 1 {
				t.Fatalf("agent_runs rows = %d, want 1: %v", len(rows), rows)
			}
			for key, raw := range rows {
				if key == "chat-shared" {
					t.Error("run state is keyed by chat id — two runs of one chat would overwrite each other")
				}
				if !ValidRunID(key) {
					t.Errorf("run state key %q is not a valid run id", key)
				}
				if tc.wantKey != "" && key != tc.wantKey {
					t.Errorf("run state key = %q, want the caller's own id %q", key, tc.wantKey)
				}
				var rs RunState
				if err := json.Unmarshal(raw, &rs); err != nil {
					t.Fatalf("unmarshal run state: %v", err)
				}
				if rs.ID != key {
					t.Errorf("RunState.ID = %q, want %q", rs.ID, key)
				}
				// Nothing is lost: the chat is still on the row.
				if rs.ChatID != "chat-shared" {
					t.Errorf("RunState.ChatID = %q, want chat-shared", rs.ChatID)
				}
			}
		})
	}
}

func TestRunAgent_RefusesAnUnsafeRunID(t *testing.T) {
	o := New(&mockContainer{}, newMemState(), slog.Default())
	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "eva",
		ChatID:      "s1",
		RunID:       "../escape",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		TimeoutSecs: 5,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid run id") {
		t.Fatalf("RunAgent with an unsafe run id: err = %v, want an 'invalid run id' failure", err)
	}
}

// TestRunAgent_TwoRunsOfOneAgentDoNotCollide drives the whole run path twice
// for ONE agent on ONE chat — the shape three dispatch paths actually produce
// (the agent webhook's constant chat id, and the peer-query / assignment reuse
// of the caller's chat id) — and asserts the two runs share no runtime name
// and no state row.
func TestRunAgent_TwoRunsOfOneAgentDoNotCollide(t *testing.T) {
	var mu sync.Mutex
	byRun := map[string][]string{}
	current := "run-a"

	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			line := strings.Join(cfg.Cmd, " ")
			// The preflight batch (mkdir of scratch / output / crew /
			// secrets, credential files, MCP config) rides ExecConfig.Stdin
			// as one `sh` script rather than argv — see preflight_batch.go —
			// so a test that only reads Cmd cannot see the scratch dir at all.
			if cfg.Stdin != nil {
				if body, err := io.ReadAll(cfg.Stdin); err == nil {
					line += "\n" + string(body)
				}
			}
			mu.Lock()
			byRun[current] = append(byRun[current], line)
			mu.Unlock()
			return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
	}
	state := newMemState()
	o := New(mc, state, slog.Default())

	run := func(runID string) {
		mu.Lock()
		current = runID
		mu.Unlock()
		if err := o.RunAgent(context.Background(), AgentRunRequest{
			AgentID:     "a1",
			AgentSlug:   "eva",
			ChatID:      "webhook-a1", // the constant, per-agent chat id
			RunID:       runID,
			ContainerID: "c1",
			CLIAdapter:  "CLAUDE_CODE",
			UserMessage: "work",
			TimeoutSecs: 30,
		}, nil); err != nil {
			t.Fatalf("RunAgent(%s): %v", runID, err)
		}
	}
	run("run-a")
	run("run-b")

	mu.Lock()
	fromB := strings.Join(byRun["run-b"], "\n")
	mu.Unlock()
	for name, aPath := range derivedPaths("eva", "run-a") {
		if strings.Contains(fromB, aPath) {
			t.Errorf("run B's exec commands name run A's %s (%q)", name, aPath)
		}
	}
	// Positive half, so the assertions above cannot pass vacuously against a
	// build that dropped run scoping altogether (both runs would then emit
	// "agent-eva" and neither would match "agent-eva-run-a").
	for name, bPath := range derivedPaths("eva", "run-b") {
		if !strings.Contains(fromB, bPath) {
			t.Errorf("run B never named its own %s (%q) — is the run id still reaching setupTmuxExec?", name, bPath)
		}
	}
	// Also: /workspace scratch is per run.
	if !strings.Contains(fromB, "/workspace/eva/run-b") {
		t.Error("run B did not get its own /workspace/eva/<runID> scratch dir")
	}
	if strings.Contains(fromB, "/workspace/eva/run-a") {
		t.Error("run B named run A's scratch dir")
	}

	rows, _ := state.List(context.Background(), "agent_runs")
	if len(rows) != 2 {
		t.Errorf("agent_runs rows = %d, want 2 (one per run, not one per chat): %v", len(rows), rows)
	}
}

// ---- every dispatch site names a run id ----

// TestEveryDispatchSiteSetsRunID walks the repository and asserts that every
// function which BUILDS an orchestrator.AgentRunRequest also names RunID.
//
// One walking test beats five per-site tests here for the reason the defect
// existed at all: the failure mode is a NEW dispatch path added later that
// forgets, and a per-site test cannot fail for a site nobody wrote yet. The
// orchestrator's own ensureRunID keeps such a path safe (it mints one), but a
// minted id correlates with nothing in the journal — so the guard is what
// keeps "safe" from quietly becoming "untraceable".
//
// Two exemptions, both principled:
//   - chatbridge's ChatInfo.ToAgentRunRequest — the ONE shared converter. It
//     takes no run id by design; each of its callers sets RunID on the result,
//     which this test checks at those callers.
//   - _test.go files, which construct requests to drive the orchestrator, not
//     to dispatch real work.
//
// Plus one named function in dispatchSiteExemptions below.
func TestEveryDispatchSiteSetsRunID(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	// Fatal, not Skip. If ../../go.mod is not there the checkout is broken,
	// and reporting "ok" would quietly delete this guard on exactly the machine
	// where something is already wrong. It also keeps the repo's skip budget
	// where it is: a skip that can never legitimately fire is pure debt.
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root not resolvable from the package dir (%v); this guard cannot run and must not report ok", err)
	}

	type violation struct{ file, fn string }
	var found []violation

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// Hidden dirs are skipped deliberately: .claude/worktrees holds
			// whole extra checkouts of this repo, and walking them reported
			// every other branch's code as a violation of this branch's rule
			// (#2194 — the same trap in internal/api's guard).
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this test cannot parse is not this test's business to
			// fail on — the compiler already has an opinion about it.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			if fn.Name.Name == "ToAgentRunRequest" || dispatchSiteExemptions[fn.Name.Name] {
				return true // see the doc comment / the exemption table
			}
			var builds, namesRunID bool
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				switch v := inner.(type) {
				case *ast.CompositeLit:
					if isAgentRunRequestType(v.Type) {
						builds = true
					}
				case *ast.CallExpr:
					if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ToAgentRunRequest" {
						builds = true
					}
				case *ast.Ident:
					if v.Name == "RunID" {
						namesRunID = true
					}
				}
				return true
			})
			if builds && !namesRunID {
				found = append(found, violation{rel, fn.Name.Name})
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk repo: %v", walkErr)
	}

	if len(found) > 0 {
		msgs := make([]string, 0, len(found))
		for _, v := range found {
			msgs = append(msgs, v.file+":"+v.fn)
		}
		sort.Strings(msgs)
		t.Errorf("these dispatch sites build an AgentRunRequest without naming RunID:\n  %s\n\n"+
			"Every run needs its own runtime identity (E0): the tmux session, the /tmp args/env/\n"+
			"script/FIFO/exit files and the run-state row are all derived from it. Pass the run id\n"+
			"this path already mints for the journal; orchestrator.NewRunID() only if it has none.",
			strings.Join(msgs, "\n  "))
	}
}

// dispatchSiteExemptions are functions that build an AgentRunRequest but never
// dispatch a RUN with it, so a run id would be meaningless.
//
// DeliverProviderLogin (codex_auth_file.go) builds a synthetic request purely
// as an argument carrier for syncLoginFile — AgentSlug + CLIAdapter +
// Credentials and nothing else — to refresh a provider login file in the
// agent's HOME outside any run. Note what that implies and is deliberately NOT
// changed here: the login file it writes lives in the agent's SHARED HOME
// (/crew/agents/<slug>), so it is not per-run. Splitting HOME is out of E0's
// mechanical scope (it is also the parent of .memory, which must stay shared);
// the MEMORY PRD §4 flags file-delivered logins as needing a per-run HOME, and
// that is a design decision this test deliberately does not pre-empt.
var dispatchSiteExemptions = map[string]bool{
	"DeliverProviderLogin": true,
}

// isAgentRunRequestType reports whether a composite-literal type is
// AgentRunRequest, written either bare (inside this package) or qualified
// (orchestrator.AgentRunRequest, everywhere else).
func isAgentRunRequestType(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "AgentRunRequest"
	case *ast.SelectorExpr:
		return v.Sel.Name == "AgentRunRequest"
	}
	return false
}

// The env file holds the run's credentials — `export ANTHROPIC_API_KEY=…` among
// them — so how long it exists on disk is a security property, not hygiene.
//
// Before per-run names this residue was invisible in the worst possible way:
// the next run of the same agent overwrote the abandoned file, which "cleaned
// up" by handing one run's credentials to the next. Unique names removed that
// accidental cleanup and would have left the file for the life of the
// container, so the delete has to be explicit — and it has to be in the inner
// script rather than only in the wrapper's exit path, because the wrapper's
// cleanup does not run when the exec is killed.
func TestSetupTmuxExec_InnerScriptUnlinksTheCredentialFileAsSoonAsItIsRead(t *testing.T) {
	o, rc := newTmuxExecOrchestrator()
	if _, err := o.setupTmuxExec(context.Background(), "c1",
		[]string{"claude", "-p", "hello"}, "eva", "run-a",
		[]string{"ANTHROPIC_API_KEY=sk-not-a-real-key"}); err != nil {
		t.Fatalf("setupTmuxExec: %v", err)
	}

	script := decodeGeneratedScript(t, rc.recorded())
	paths := derivedPaths("eva", "run-a")
	// Looked up loudly: a renamed key would otherwise leave these empty and the
	// searches below would match nothing while the test still passed.
	envFile, argsFile := mustPath(t, paths, "env file"), mustPath(t, paths, "args file")

	sourceAt := strings.Index(script, ". '"+envFile+"'")
	if sourceAt < 0 {
		t.Fatalf("the inner script never sources the env file:\n%s", script)
	}
	removeAt := strings.Index(script, "rm -f '"+envFile+"'")
	if removeAt < 0 {
		t.Fatalf("the inner script never removes the env file, so the run's credentials "+
			"survive for the life of the container:\n%s", script)
	}
	if removeAt < sourceAt {
		t.Fatalf("the env file is removed before it is sourced; the run would start with no environment:\n%s", script)
	}

	// Nothing may run between the source and the delete: any command in between
	// is a window in which an abandoned run leaves credentials behind.
	between := strings.TrimSpace(script[sourceAt+len(". '"+envFile+"'") : removeAt])
	if between != "" {
		t.Errorf("commands run between sourcing the env file and deleting it: %q", between)
	}

	// The prompt goes the same way once xargs has consumed it. Not a
	// credential, but not public either.
	if !strings.Contains(script, "rm -f '"+argsFile+"'") {
		t.Errorf("the inner script never removes the args file:\n%s", script)
	}
}

func mustPath(t *testing.T, paths map[string]string, key string) string {
	t.Helper()
	v, ok := paths[key]
	if !ok || v == "" {
		t.Fatalf("derivedPaths has no %q; this test would otherwise search for an empty string and pass", key)
	}
	return v
}

// decodeGeneratedScript pulls the inner script back out of the batched write
// command. The script is base64'd into the exec, so a test that searched the
// raw command would silently match nothing and pass.
func decodeGeneratedScript(t *testing.T, recorded []string) string {
	t.Helper()
	joined := strings.Join(recorded, "\n")
	var best string
	for _, field := range strings.Split(joined, "'") {
		decoded, err := base64.StdEncoding.DecodeString(field)
		if err != nil {
			continue
		}
		if strings.HasPrefix(string(decoded), "#!/bin/sh") && len(decoded) > len(best) {
			best = string(decoded)
		}
	}
	if best == "" {
		t.Fatalf("no base64 payload in the generated commands decoded to a shell script:\n%s", joined)
	}
	return best
}

// HOME cleanup must not erase the launch identity needed to settle a cancel.
func TestRunLocation_ProbeSurvivesHomeRelease(t *testing.T) {
	location := RunLocation{ContainerID: "probe-container", AgentSlug: "eva", RunID: "probe-run"}
	retainRunHome(location.ContainerID, location.AgentSlug, location.RunID)
	releaseRunHome(location.ContainerID, location.AgentSlug, location.RunID)
	calls := 0
	o := New(&mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		calls++
		if cfg.ContainerID != location.ContainerID || !strings.Contains(strings.Join(cfg.Cmd, " "), TmuxSessionName(location.AgentSlug, location.RunID)) {
			t.Fatalf("probe targeted a different runtime: %+v", cfg)
		}
		return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader("ABSENT\n"))}, nil
	}}, newMemState(), slog.Default())
	if _, err := o.RunIsAlive(context.Background(), location.RunID); err == nil {
		t.Fatal("HOME registry unexpectedly retained released run")
	}
	alive, err := o.RunIsAliveAt(context.Background(), location)
	if err != nil || alive {
		t.Fatalf("released HOME probe: alive=%v err=%v", alive, err)
	}
	stopped, err := o.StopRunAt(context.Background(), location)
	if err != nil || !stopped {
		t.Fatalf("released HOME stop: stopped=%v err=%v", stopped, err)
	}
	if calls != 2 {
		t.Fatalf("container probes=%d, want 2", calls)
	}
}
