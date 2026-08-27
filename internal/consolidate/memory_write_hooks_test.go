package consolidate

// memory_write_hooks_test.go proves pre_memory_write and
// post_memory_write — two of the ten hook events found alongside
// pre_tool_call (#2132) to be declared in hooks.AllEvents, accepted by the
// CLI/API, and reached by zero hooks.Dispatch call sites — now actually
// fire around Consolidator.Run's canonical write (appendRules), the real
// "memory consolidation path" the hooks guide's own coverage table always
// named as the wiring target for these two events.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
)

// TestConsolidator_DispatchesPreAndPostMemoryWriteHooks registers a
// blocking webhook on each event and runs a normal consolidation tick
// (same fixture as TestConsolidator_WritesLearnedMarkdownAndEmitsEntry)
// end to end, asserting both fired in order around the write.
func TestConsolidator_DispatchesPreAndPostMemoryWriteHooks(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1

	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"frequent escalations to lead","action":"pre-brief leads","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.8}]`

	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	for _, ev := range []hooks.Event{hooks.EventPreMemoryWrite, hooks.EventPostMemoryWrite} {
		if _, err := hooks.Register(context.Background(), db, hooks.Hook{
			WorkspaceID:   "ws_test",
			Event:         ev,
			HandlerKind:   hooks.HandlerKindHTTP,
			HandlerConfig: map[string]any{"url": ts.URL + "/" + string(ev)},
			Blocking:      true,
			Enabled:       true,
		}, false); err != nil {
			t.Fatalf("register %s hook: %v", ev, err)
		}
	}

	stub := &stubSummarizer{Reply: reply}
	c := &Consolidator{DB: db, Journal: w, Summarizer: stub, Logger: quietLogger()}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   t.TempDir(),
		LLMModel:    "stub-model",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RulesAppended != 1 {
		t.Fatalf("rules appended = %d, want 1", res.RulesAppended)
	}

	if len(seen) != 2 {
		t.Fatalf("hook hits = %v, want exactly [pre_memory_write, post_memory_write]", seen)
	}
	if seen[0] != "/pre_memory_write" || seen[1] != "/post_memory_write" {
		t.Errorf("hook order = %v, want [/pre_memory_write /post_memory_write]", seen)
	}
}

// TestConsolidator_PreMemoryWriteHookBlocksTheWrite proves a blocking
// pre_memory_write hook refuses the consolidator's write before
// appendRules ever touches disk — a future write-approval gate for agent
// memory hangs off exactly this seam.
func TestConsolidator_PreMemoryWriteHookBlocksTheWrite(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true")

	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"frequent escalations to lead","action":"pre-brief leads","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.8}]`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503) // Block outcome
	}))
	defer ts.Close()

	if _, err := hooks.Register(context.Background(), db, hooks.Hook{
		WorkspaceID:   "ws_test",
		Event:         hooks.EventPreMemoryWrite,
		HandlerKind:   hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{"url": ts.URL},
		Blocking:      true,
		Enabled:       true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	stub := &stubSummarizer{Reply: reply}
	c := &Consolidator{DB: db, Journal: w, Summarizer: stub, Logger: quietLogger()}
	tmp := t.TempDir()
	_, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   tmp,
		LLMModel:    "stub-model",
	})
	if err == nil {
		t.Fatal("expected the blocking pre_memory_write hook to refuse the write")
	}

	entries, rerr := os.ReadDir(tmp)
	if rerr != nil {
		t.Fatalf("readdir: %v", rerr)
	}
	if len(entries) != 0 {
		t.Errorf("expected no learned-*.md written when pre_memory_write blocks, got %v", entries)
	}
}
