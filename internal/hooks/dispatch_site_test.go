package hooks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
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
// that gap: post_tool_call now correctly reports no dispatch site (see
// preExistingGaps below).
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

	// preExistingGaps: events that already had zero production dispatch
	// site before this test was written, discovered by the same
	// investigation that removed pre_tool_call from AllEvents. Fixing
	// eleven independently-dead events at once is out of scope for that
	// change, so they are logged rather than failed here — but they are
	// not swept under the rug: every one of them is named below, with the
	// same "no dispatch site" finding this test would otherwise report as
	// a failure. Removing an event from this map is part of the diff that
	// wires up its dispatch, the same discipline this test applies to
	// every event added from here on.
	//
	// post_tool_call is deliberately NOT in this map: closing the
	// false-positive gap above makes this test newly (and correctly)
	// report it as undispatched, same as the other nine.
	preExistingGaps := map[Event]bool{
		EventPostToolCall:         true,
		EventPreTaskDelegation:    true,
		EventPostTaskDelegation:   true,
		EventPreLLMCall:           true,
		EventPostLLMCall:          true,
		EventPreMemoryWrite:       true,
		EventPostMemoryWrite:      true,
		EventPrePeerConversation:  true,
		EventPostPeerConversation: true,
		EventOnBudgetExceeded:     true,
	}

	var undispatched []string
	for _, ev := range AllEvents {
		if eventHasDispatchSite(ev, files, constNames[ev]) {
			continue
		}
		if preExistingGaps[ev] {
			t.Logf("known pre-existing gap (not introduced by this test, not fixed by it either): %q has no dispatch site outside internal/hooks", ev)
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
// preExistingGaps — the moment one of those events gets a real Dispatch
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
			case ".git", "node_modules", "web":
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
