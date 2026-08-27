package hooks

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
// The scan looks, in every non-test .go file outside this package, for
// either:
//   - the qualified identifier hooks.Event<PascalName> (covers call sites
//     that pass the typed constant straight into Dispatch, e.g.
//     runner_llm.go's hooks.Dispatch(..., hooks.EventOnGuardrailTriggered, ...)), or
//   - the event's snake_case string literal on a line that also contains
//     "Dispatch(" (covers the generic string-typed pass-through in
//     internal/server/orchestrator_adapters.go, which forwards a plain
//     string the orchestrator supplies at the call site, not a typed
//     constant).
//
// This is a heuristic, not full data-flow analysis — a file that merely
// mentions hooks.EventXxx without ever feeding it to Dispatch would still
// count as "found". That imprecision is deliberate: the alternative is a
// hand-maintained event -> dispatch-site table that has to be edited by
// hand every time a call site moves, which is the same kind of silent
// drift this test exists to catch. Known cases where the heuristic is
// looser than reality are called out inline below.
func TestEveryOfferedEventHasADispatchSite(t *testing.T) {
	root := repoRootForTest(t)
	files := collectScanFiles(t, root)

	// No escape hatch: every event in AllEvents is required to have a real
	// dispatch site. This test used to carry a preExistingGaps allowlist —
	// nine events (pre/post_task_delegation, pre/post_llm_call,
	// pre/post_memory_write, pre/post_peer_conversation,
	// on_budget_exceeded) found undispatched by the same investigation
	// that removed pre_tool_call, logged rather than failed because fixing
	// all of them was out of scope for that change. That follow-up
	// landed: every one of the nine now has a genuine Dispatch call
	// (runAssignment, llm.Middleware's hooksCaller, Consolidator.Run,
	// QueryHandler.Create/finishQuery, paymaster.Enforce), and
	// post_tool_call — declared covered by the heuristic all along
	// because internal/server referenced the EventPostToolCall constant
	// as a struct field without ever calling Dispatch — now has a real
	// one too (postToolCallObserver.Observe). Removing the allowlist is
	// what turns "we know about these gaps" into "these gaps cannot
	// recur without a red test": a future event added to AllEvents with
	// no Dispatch call now fails here immediately, the same as it would
	// have for pre_tool_call had this test existed sooner.
	var undispatched []string
	for _, ev := range AllEvents {
		if eventHasDispatchSite(ev, files) {
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
// TestEveryOfferedEventHasADispatchSite.
func eventHasDispatchSite(ev Event, files []scannedFile) bool {
	goName := "hooks." + "Event" + snakeToPascal(string(ev))
	identRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(goName) + `\b`)
	literal := `"` + string(ev) + `"`

	for _, f := range files {
		if identRe.MatchString(f.content) {
			return true
		}
		for _, line := range strings.Split(f.content, "\n") {
			if strings.Contains(line, "Dispatch(") && strings.Contains(line, literal) {
				return true
			}
		}
	}
	return false
}

// initialisms are the snake_case segments the Event constants spell fully
// upper-case, per Go's own initialism convention (the same list style
// golint/staticcheck ship) — a plain "capitalize the first letter" pass
// gets "pre_llm_call" to "PreLlmCall", but the real constant is
// EventPreLLMCall. Extend this map if a future event's segment needs the
// same treatment; the failure mode of forgetting to is a false-negative
// "no dispatch site" from this test even when one exists, so it's easy to
// notice and cheap to fix here rather than by hand-listing a gap.
var initialisms = map[string]string{
	"llm": "LLM",
}

// snakeToPascal turns "on_guardrail_triggered" into "OnGuardrailTriggered"
// — the same convention the Event constants above already follow, derived
// rather than hand-listed so a newly added event needs no parallel entry
// here to be checked.
func snakeToPascal(snake string) string {
	parts := strings.Split(snake, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if up, ok := initialisms[p]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

type scannedFile struct {
	path    string
	content string
}

// collectScanFiles reads every non-test .go file in the repo except this
// package's own (internal/hooks defines the Event constants themselves,
// which would trivially "find" every event regardless of whether anything
// actually dispatches it).
func collectScanFiles(t *testing.T, root string) []scannedFile {
	t.Helper()
	hooksDir := filepath.Join(root, "internal", "hooks") + string(filepath.Separator)

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
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil // unreadable file can't be a dispatch site either way
		}
		out = append(out, scannedFile{path: path, content: string(data)})
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
