package hooks

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEveryOfferedEventHasADispatchSite is a source-scan invariant, not a
// hand-maintained table: an event that ValidateEvent accepts — i.e. is in
// AllEvents, and therefore offered to users through `crewship hooks create
// --event ...` and POST /api/v1/hooks — has to be reachable from at least
// one real Dispatch call somewhere in the tree, or a hook registered
// against it is permanently dead. That registration still returns 201,
// still lists, still shows enabled — nothing about it looks wrong until
// someone notices the handler never runs. pre_tool_call was exactly this
// bug: declared in AllEvents, accepted by ValidateEvent, and dispatched by
// nothing.
//
// The scan parses every non-test .go file outside this package with
// go/parser and looks for a call expression whose selector is named
// "Dispatch" (hooks.Dispatch itself, or an interface method like
// orchestrator.HookDispatcher.Dispatch that hooks.Dispatch sits behind —
// see orchestrator_run.go calling o.getHooks().Dispatch(ctx,
// "pre_agent_start", ...) rather than hooks.Dispatch directly) that takes,
// as one of its arguments, either:
//
//   - the qualified identifier hooks.Event<PascalName> (call sites that
//     pass the typed constant straight in, e.g. runner_llm.go's
//     hooks.Dispatch(..., hooks.EventOnGuardrailTriggered, ...)), or
//   - the event's snake_case string literal (the generic string-typed
//     pass-through in orchestrator_run.go, which calls the
//     HookDispatcher interface with a plain string literal rather than a
//     typed constant).
//
// This used to be a regexp.MatchString over each file's ENTIRE raw text,
// which meant the qualified-identifier branch matched a file that merely
// MENTIONED hooks.EventXxx anywhere, dispatch or not. post_tool_call
// slipped through exactly that gap: its only mentions outside this package
// are `Event: hooks.EventPostToolCall,` struct-field assignments in
// internal/server/post_tool_call_adapter.go and
// internal/keeper/behaviorhook/behaviorhook.go — an unrelated sampling
// subsystem that never calls Dispatch — and the bare-presence regexp
// counted those as a dispatch site. Requiring the identifier (or literal)
// to actually be an ARGUMENT of a call to something named Dispatch closes
// that gap: post_tool_call stopped reading as covered until Observe was
// given a real hooks.Dispatch call.
//
// This is still a heuristic, not full data-flow analysis — a value routed
// through an intermediate variable before reaching Dispatch, or a Dispatch
// call on a wrapper type whose events are computed rather than literal,
// would not be seen. That imprecision is deliberate: the alternative is a
// hand-maintained event -> dispatch-site table that has to be edited by
// hand every time a call site moves, which is the same kind of silent
// drift this test exists to catch.
func TestEveryOfferedEventHasADispatchSite(t *testing.T) {
	root := repoRootForTest(t)
	files := collectScanFiles(t, root)
	constNames := collectEventConstNames(t, root)

	// No escape hatch: every event in AllEvents is required to have a real
	// dispatch site. This test used to carry a preExistingDispatchGaps allowlist —
	// ten events (post_tool_call, pre/post_task_delegation, pre/post_llm_call,
	// pre/post_memory_write, pre/post_peer_conversation, on_budget_exceeded)
	// found undispatched by the AST-based rewrite of eventHasDispatchSite,
	// which closed the false-positive that had let post_tool_call read as
	// covered (see the package doc above): internal/server and
	// internal/keeper/behaviorhook only ever referenced the
	// EventPostToolCall constant as a struct field, never called Dispatch
	// with it. Logged rather than failed at the time because fixing all
	// ten was out of scope for that change. That follow-up landed: every
	// one of the ten now has a genuine Dispatch call passing the typed
	// constant directly (runAssignment's pre/post_task_delegation,
	// llm.Middleware's hooksCaller for pre/post_llm_call,
	// Consolidator.Run's appendRules write for pre/post_memory_write,
	// QueryHandler.Create/finishQuery for pre/post_peer_conversation,
	// paymaster.Enforce for on_budget_exceeded, and
	// postToolCallObserver.Observe for post_tool_call). Removing the
	// allowlist is what turns "we know about these gaps" into "these gaps
	// cannot recur without a red test": a future event added to AllEvents
	// with no Dispatch call now fails here immediately, the same as it
	// would have for pre_tool_call had this test existed sooner.
	var undispatched []string
	for _, ev := range AllEvents {
		if eventHasDispatchSite(ev, files, constNames[ev]) {
			continue
		}
		undispatched = append(undispatched, string(ev))
	}

	if len(undispatched) > 0 {
		sort.Strings(undispatched)
		t.Errorf("event(s) offered to users (in hooks.AllEvents) with no dispatch site anywhere outside internal/hooks: %s\n"+
			"each of these registers cleanly via Register/the API and then never fires — either wire a real Dispatch call "+
			"for it, or remove it from AllEvents the way pre_tool_call was removed.", strings.Join(undispatched, ", "))
	}
}

// eventHasDispatchSite reports whether ev has at least one plausible
// dispatch site among files, per the heuristic documented on
// TestEveryOfferedEventHasADispatchSite. constName is the Go identifier
// declared for ev in internal/hooks/types.go (e.g. "EventPreLLMCall" for
// ev == EventPreLLMCall), as discovered by collectEventConstNames — an
// empty constName just disables the identifier branch and falls back to
// the string-literal branch.
func eventHasDispatchSite(ev Event, files []scannedFile, constName string) bool {
	literal := string(ev)

	for _, f := range files {
		if f.astFile == nil {
			continue // unparseable file already reported via t.Logf; skip rather than crash the scan
		}
		found := false
		ast.Inspect(f.astFile, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Dispatch" {
				return true
			}
			for _, arg := range call.Args {
				if dispatchArgMatchesEvent(arg, constName, literal) {
					found = true
					return false
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// dispatchArgMatchesEvent reports whether arg — one argument of a call to
// something named Dispatch — is evidence that call dispatches ev.
func dispatchArgMatchesEvent(arg ast.Expr, constName, literal string) bool {
	switch v := arg.(type) {
	case *ast.SelectorExpr:
		// hooks.EventPreLLMCall — the typed constant passed straight in.
		id, ok := v.X.(*ast.Ident)
		return ok && id.Name == "hooks" && constName != "" && v.Sel.Name == constName
	case *ast.BasicLit:
		// "pre_llm_call" — the generic string-typed pass-through.
		if v.Kind != token.STRING {
			return false
		}
		s, err := strconv.Unquote(v.Value)
		return err == nil && s == literal
	default:
		return false
	}
}

// collectEventConstNames maps each Event value to the Go identifier its
// `const` declaration uses in internal/hooks (e.g. "pre_llm_call" ->
// "EventPreLLMCall"), by parsing the package's own non-test source rather
// than guessing the identifier from the snake_case value.
//
// An earlier version of this test derived the identifier with a
// snake_case -> PascalCase helper (snakeToPascal) that got acronyms wrong:
// snakeToPascal("pre_llm_call") produced "PreLlmCall", but the real
// constant is EventPreLLMCall. That bug was silent only because every
// event whose name contains an acronym (LLM) was, at the time, also in
// preExistingDispatchGaps — the moment one of those events gets a real Dispatch
// call site wired up, the guessed name stops matching the real one and
// the test fails with a confusing "no dispatch site" even though a
// dispatch site exists. Reading the identifier directly out of the const
// declaration is correct for every acronym, current or future, without
// needing a parallel spelling table to keep in sync.
func collectEventConstNames(t *testing.T, root string) map[Event]string {
	t.Helper()
	hooksDir := filepath.Join(root, "internal", "hooks")

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		t.Fatalf("read %s: %v", hooksDir, err)
	}

	fset := token.NewFileSet()
	out := map[Event]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(hooksDir, name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				typeIdent, ok := vs.Type.(*ast.Ident)
				if !ok || typeIdent.Name != "Event" {
					continue
				}
				for i, valExpr := range vs.Values {
					lit, ok := valExpr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if i < len(vs.Names) {
						out[Event(s)] = vs.Names[i].Name
					}
				}
			}
		}
	}
	return out
}

type scannedFile struct {
	path    string
	astFile *ast.File // nil if the file failed to parse — skipped, not fatal
}

// collectScanFiles reads and parses every non-test .go file in the repo
// except this package's own (internal/hooks defines the Event constants
// themselves, which would trivially "find" every event regardless of
// whether anything actually dispatches it).
func collectScanFiles(t *testing.T, root string) []scannedFile {
	t.Helper()
	hooksDir := filepath.Join(root, "internal", "hooks") + string(filepath.Separator)
	fset := token.NewFileSet()

	var out []scannedFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			// `.claude` holds agent worktrees: whole copies of this tree. This
			// guard's assertion happens not to be keyed by path, so the
			// copies pass rather than fail — but it re-walks one whole
			// tree per worktree to answer for one, and stops being
			// harmless the day it grows a path-keyed exemption (#2188).
			case ".git", ".claude", "node_modules", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, hooksDir) {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			// A handful of generated or build-tagged files may not parse
			// with a bare parser.ParseFile (no build constraints applied).
			// Log and skip rather than fail the whole invariant on a file
			// that was never going to contain a Dispatch call anyway.
			t.Logf("dispatch-site scan: skipping unparseable file %s: %v", path, parseErr)
			f = nil
		}
		out = append(out, scannedFile{path: path, astFile: f})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("scanned zero source files under %s — the walk is almost certainly misconfigured", root)
	}
	return out
}

// repoRootForTest walks up from the test's working directory (the package
// directory under `go test`) until it finds go.mod, so the scan works
// whether the module lives at a plain checkout path or inside a worktree.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// TestDispatchSiteScanIsNotVacuous is the anti-vacuity guard that used to
// live in TestPreExistingDispatchGapsAreStillGaps. With the gap allowlist
// gone, TestEveryOfferedEventHasADispatchSite passes when the scan finds a
// site for all fourteen events — and would ALSO pass if a broken scan
// somehow reported "found" for everything. So pin both directions: the scan
// must be able to answer "no", and it must find a real site through each of
// its two matching branches.
func TestDispatchSiteScanIsNotVacuous(t *testing.T) {
	root := repoRootForTest(t)
	files := collectScanFiles(t, root)
	constNames := collectEventConstNames(t, root)

	// "no" direction: an event nothing has ever declared has neither a
	// constant nor a literal anywhere in the tree. A scan that reports a
	// site for it is matching something other than what it claims, and
	// every pass above means nothing.
	if eventHasDispatchSite(Event("no_such_event_9f2c1a"), files, "") {
		t.Fatal("the dispatch-site scan found a site for an event that does not exist — " +
			"it is matching something other than a Dispatch argument, so every other " +
			"assertion in this file is vacuous")
	}

	// pre_tool_call is not in AllEvents, so the loop above never looks at
	// it. If a dispatch site for it ever appears, the constant belongs back
	// in AllEvents — a live dispatch site for an event nobody can register
	// is the mirror image of the bug this file exists for.
	if eventHasDispatchSite(EventPreToolCall, files, constNames[EventPreToolCall]) {
		t.Errorf("pre_tool_call now has a dispatch site but is absent from AllEvents — " +
			"nobody can register a hook for it, so the site is dead code; put the constant " +
			"back in AllEvents (and update the docs) or remove the dispatch call")
	}

	// "yes" direction, both branches. pre_agent_start is only ever
	// dispatched as a bare string literal through the orchestrator's
	// HookDispatcher interface; on_guardrail_triggered is only ever
	// dispatched as the typed constant. If either branch regresses, one of
	// these goes red with a precise cause instead of the whole event list
	// going red with a vague one.
	if !eventHasDispatchSite(EventPreAgentStart, files, constNames[EventPreAgentStart]) {
		t.Error("string-literal branch of the scan found no site for pre_agent_start " +
			"(orchestrator_run.go dispatches it as a plain string)")
	}
	if !eventHasDispatchSite(EventOnGuardrailTriggered, files, constNames[EventOnGuardrailTriggered]) {
		t.Error("typed-constant branch of the scan found no site for on_guardrail_triggered " +
			"(runner_llm.go dispatches hooks.EventOnGuardrailTriggered)")
	}

	// The acronym fix, pinned directly rather than via its symptom: the
	// constant name is read out of the const declaration, so an event whose
	// value contains an acronym resolves to the real identifier and not a
	// snakeToPascal guess ("PreLlmCall").
	if got := constNames[EventPreLLMCall]; got != "EventPreLLMCall" {
		t.Errorf("constNames[pre_llm_call] = %q, want %q — the identifier is being guessed, "+
			"not read from the const declaration, and acronym events will read as permanent gaps", got, "EventPreLLMCall")
	}
}

// ---------------------------------------------------------------------
// The dynamic half: registration is not observation
// ---------------------------------------------------------------------

// TestEveryOfferedEventActuallyReachesItsHandler is the counterweight to
// the source scan above. The scan answers "is there a Dispatch call for
// this event somewhere?"; it cannot answer "and if that call runs, does a
// hook registered on the event actually execute?" — which is the half that
// let pre_tool_call ship. Every test that covered it asserted the
// REGISTRATION succeeded (201, row present, ValidateEvent happy); nothing
// asserted a handler ran. So this one registers a real hook per event and
// fails unless the handler is hit.
//
// Gate-capable events (Event.SupportsBlocking) are registered blocking, so
// Dispatch runs the handler inline and the assertion is a plain read.
// Observation events cannot be registered blocking any more — Register
// rejects that with ErrEventCannotBlock — and their handlers run in a
// goroutine Dispatch never waits on, so those subtests synchronise on the
// handler itself. Reading hits.Load() straight after Dispatch would pass
// for the wrong reason there: it would be a race, not an assertion, and a
// hook that never fired at all would look identical to one that had not
// fired yet.
//
// It also pins the other end of the original failure mode: the bug report
// for the misspelled-event class was "inserts cleanly, then never selected
// by ListByEvent". So each subtest additionally dispatches a DIFFERENT
// event and requires the hook not to fire — proving the event value
// round-trips through Register -> hooks_config -> ListByEvent -> Dispatch
// as an exact match rather than firing on everything (or nothing).
func TestEveryOfferedEventActuallyReachesItsHandler(t *testing.T) {
	observed := 0

	for _, ev := range AllEvents {
		t.Run(string(ev), func(t *testing.T) {
			t.Setenv(allowPrivateEnvVar, "true") // httptest listens on loopback
			db := openTestDB(t)
			defer db.Close()
			ctx := context.Background()

			var hits atomic.Int64
			fired := make(chan struct{}, 8)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
				select {
				case fired <- struct{}{}:
				default:
				}
			}))
			defer ts.Close()

			blocking := ev.SupportsBlocking()
			if _, err := Register(ctx, db, Hook{
				WorkspaceID:   "ws_test",
				Event:         ev,
				HandlerKind:   HandlerKindHTTP,
				HandlerConfig: map[string]any{"url": ts.URL},
				Blocking:      blocking,
				Enabled:       true,
			}, false); err != nil {
				t.Fatalf("Register(%q) = %v — every event in AllEvents must be registerable", ev, err)
			}

			rec := &recordingEmitter{}
			if err := Dispatch(ctx, db, rec, ev, EventContext{WorkspaceID: "ws_test"}); err != nil {
				t.Fatalf("Dispatch(%q) = %v", ev, err)
			}
			if blocking {
				// Ran inline: the handler must already have been hit.
				if got := hits.Load(); got != 1 {
					t.Fatalf("handler ran %d times for %q, want 1 — the hook registered fine and never fired, "+
						"which is exactly the pre_tool_call failure mode", got, ev)
				}
			}
			select {
			case <-fired:
			case <-time.After(10 * time.Second):
				t.Fatalf("handler never ran for %q — the hook registered fine and never fired, "+
					"which is exactly the pre_tool_call failure mode", ev)
			}

			// A different event must NOT reach this hook.
			other := EventPreAgentStart
			if ev == EventPreAgentStart {
				other = EventPostAgentStop
			}
			if err := Dispatch(ctx, db, rec, other, EventContext{WorkspaceID: "ws_test"}); err != nil {
				t.Fatalf("Dispatch(%q) = %v", other, err)
			}
			select {
			case <-fired:
				t.Errorf("hook registered on %q also fired for %q (%d total hits) — "+
					"ListByEvent is not matching on the exact event value", ev, other, hits.Load())
			case <-time.After(250 * time.Millisecond):
			}

			observed++
		})
	}

	// Anti-vacuity, and it is read: a t.Run body that never executes (or
	// an empty AllEvents) would leave every assertion above unrun while
	// the test still reported PASS.
	if observed == 0 || observed != len(AllEvents) {
		t.Fatalf("observed a firing handler for %d of %d events in AllEvents — "+
			"the loop did not actually exercise every event", observed, len(AllEvents))
	}
}

// TestPreToolCallCannotBeRegisteredAtAll is the negative half of the same
// question. There is no "did it fire" to assert for pre_tool_call, because
// the fix makes the registration itself impossible — so assert that, and
// assert nothing lands in hooks_config, rather than leaving the retirement
// pinned only by the doc comment on AllEvents.
func TestPreToolCallCannotBeRegisteredAtAll(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := ValidateEvent(EventPreToolCall); !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("ValidateEvent(pre_tool_call) = %v, want ErrUnknownEvent", err)
	}
	for _, name := range EventNames() {
		if name == string(EventPreToolCall) {
			t.Fatalf("EventNames() still advertises %q to the CLI and API", name)
		}
	}

	_, err := Register(ctx, db, Hook{
		WorkspaceID:   "ws_test",
		Event:         EventPreToolCall,
		HandlerKind:   HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": "https://example.test/h"},
		Enabled:       true,
	}, false)
	if !errors.Is(err, ErrUnknownEvent) {
		t.Fatalf("Register(pre_tool_call) = %v, want ErrUnknownEvent", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hooks_config`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("pre_tool_call hook landed in hooks_config anyway (%d rows)", n)
	}
}
