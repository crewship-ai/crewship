package consolidate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
)

// testSchema is the minimum DDL needed to exercise the consolidate
// package against a real sqlite DB without bringing in the full
// migrate.go. The columns mirror migration 52's journal_entries exactly
// so the production INSERT / SELECT statements work unchanged.
const testSchema = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    slug TEXT NOT NULL,
    deleted_at TEXT
);
CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    ts TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    priority TEXT NOT NULL DEFAULT 'normal',
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    summary TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    refs TEXT NOT NULL DEFAULT '{}',
    trace_id TEXT,
    span_id TEXT,
    expires_at TEXT
);
CREATE INDEX idx_journal_ws_ts ON journal_entries(workspace_id, ts DESC);
INSERT INTO workspaces (id) VALUES ('ws_test');
INSERT INTO crews (id, workspace_id, slug) VALUES ('crew_test', 'ws_test', 'crew-test');
`

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	// Match the journal package's test setup exactly: ":memory:" DSN
	// with a single-connection pool. modernc.org/sqlite implements
	// ":memory:" per connection, so without MaxOpenConns(1) the journal
	// writer goroutine would get a different in-memory DB than the
	// reader. One shared connection means one shared DB, which is
	// exactly what these tests need.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), testSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubSummarizer returns a pre-canned response and records the prompts
// it was called with. The response is swappable per-test by setting
// Reply (or ReplyFn for dynamic responses).
type stubSummarizer struct {
	Reply   string
	ReplyFn func(prompt string) (string, error)
	mu      sync.Mutex
	calls   []string
}

func (s *stubSummarizer) Summarize(ctx context.Context, prompt string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, prompt)
	s.mu.Unlock()
	if s.ReplyFn != nil {
		return s.ReplyFn(prompt)
	}
	return s.Reply, nil
}

func (s *stubSummarizer) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// seedEntries pushes n synthetic candidate entries so the consolidator
// has something to chew on. Each is of a type the consolidator considers
// semantically interesting (peer.escalation) so the filter keeps them.
//
// Writes go directly through the DB handle, not through the async
// journal.Writer batcher. Going synchronous here removes the need for
// sleep-after-Flush test workarounds and keeps the test strictly
// deterministic — every returned ID is already visible to a subsequent
// SELECT.
func seedEntries(t *testing.T, db *sql.DB, _ journal.Emitter, workspaceID, crewID string, n int, kind journal.EntryType) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := "j_seed_" + itoa3(i)
		ts := time.Now().UTC().Add(-time.Duration(i) * time.Minute)
		_, err := db.ExecContext(context.Background(),
			`INSERT INTO journal_entries
			 (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
			 VALUES (?, ?, ?, ?, ?, 'info', 'agent', 'agent_x', 'seeded entry', '{}', '{}')`,
			id, workspaceID, crewID, ts.Format(time.RFC3339Nano), string(kind))
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestConsolidator_SkipWhenBelowThreshold(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// Only 3 entries; threshold is 10.
	seedEntries(t, db, w, "ws_test", "crew_test", 3, journal.EntryPeerEscalation)

	c := &Consolidator{
		DB:         db,
		Journal:    w,
		Summarizer: &stubSummarizer{Reply: "[]"},
		Logger:     quietLogger(),
	}
	tmp := t.TempDir()
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   tmp,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected skipped, got %+v", res)
	}
	if res.RulesAppended != 0 || res.OutputPath != "" {
		t.Errorf("skip should not produce output: %+v", res)
	}
	// No summarizer call when skipped.
	if stub := c.Summarizer.(*stubSummarizer); len(stub.Calls()) != 0 {
		t.Errorf("summarizer was called during skip: %d", len(stub.Calls()))
	}
	// No file should be created.
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Errorf("expected empty dir, got %d files", len(entries))
	}
}

func TestConsolidator_WritesLearnedMarkdownAndEmitsEntry(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	// Build a reply that references two real entry IDs per rule so the
	// evidence-count filter keeps them.
	reply := `[
      {"pattern":"frequent escalations to lead","action":"pre-brief leads on open blockers","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.8},
      {"pattern":"missing context on handoff","action":"attach last summary to each handoff","evidence":["` + ids[2] + `","` + ids[3] + `","` + ids[4] + `"],"confidence":0.72}
    ]`

	stub := &stubSummarizer{Reply: reply}
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: stub, Logger: quietLogger(),
	}
	tmp := t.TempDir()
	ctx := context.Background()
	res, err := c.Run(ctx, Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   tmp,
		LLMModel:    "stub-model",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Skipped {
		t.Fatalf("should not skip: %+v", res)
	}
	if res.RulesAppended != 2 {
		t.Fatalf("rules: got %d want 2", res.RulesAppended)
	}

	b, err := os.ReadFile(res.OutputPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		"# Learned rules",
		"frequent escalations to lead",
		"pre-brief leads on open blockers",
		"missing context on handoff",
		ids[0], ids[1], ids[2], ids[3], ids[4],
	} {
		if !strings.Contains(body, want) {
			t.Errorf("markdown missing %q in:\n%s", want, body)
		}
	}

	// Verify the memory.consolidated journal entry was emitted with the
	// right payload + refs.
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	entries, _, err := journal.List(ctx, db, journal.Query{
		WorkspaceID: "ws_test",
		Types:       []journal.EntryType{journal.EntryMemoryConsolidated},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 memory.consolidated, got %d", len(entries))
	}
	if entries[0].ID != res.JournalEntryID {
		t.Errorf("journal id mismatch: %s vs %s", entries[0].ID, res.JournalEntryID)
	}
	if got, _ := entries[0].Payload["rules_count"].(float64); int(got) != 2 {
		t.Errorf("rules_count payload: %v", entries[0].Payload["rules_count"])
	}
	if got, _ := entries[0].Payload["entries_scanned"].(float64); int(got) != 12 {
		t.Errorf("entries_scanned payload: %v", entries[0].Payload["entries_scanned"])
	}
	// Evidence is flattened and deduplicated into refs.source_entry_ids.
	srcRaw, ok := entries[0].Refs["source_entry_ids"].([]any)
	if !ok {
		t.Fatalf("refs.source_entry_ids missing or wrong type: %v", entries[0].Refs)
	}
	if len(srcRaw) != 5 {
		t.Errorf("expected 5 unique evidence ids, got %d", len(srcRaw))
	}
}

func TestConsolidator_AppendsOnSameDay(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	// Each call returns a distinct pattern so dedupAgainstPrior does
	// not eat the second invocation's rule (it scans the same-day
	// learned-*.md for the first call's pattern hash).
	var callIdx int
	stub := &stubSummarizer{ReplyFn: func(string) (string, error) {
		callIdx++
		return `[{"pattern":"pattern-` + fmt.Sprintf("%d", callIdx) + `","action":"a","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.5}]`, nil
	}}
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: stub, Logger: quietLogger(),
	}
	tmp := t.TempDir()
	cfg := Config{WorkspaceID: "ws_test", CrewID: "crew_test", Since: time.Hour, MinEntries: 10, OutputDir: tmp}

	first, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.OutputPath != second.OutputPath {
		t.Fatalf("expected same daily file, got %s and %s", first.OutputPath, second.OutputPath)
	}

	b, _ := os.ReadFile(second.OutputPath)
	body := string(b)
	// Header must appear exactly once; divider between runs must appear.
	if got := strings.Count(body, "# Learned rules"); got != 1 {
		t.Errorf("header count: got %d want 1", got)
	}
	if !strings.Contains(body, "---") {
		t.Errorf("expected divider between runs:\n%s", body)
	}
	if got := strings.Count(body, "## Run at"); got != 2 {
		t.Errorf("run sections: got %d want 2", got)
	}
}

func TestConsolidator_MalformedLLMResponse(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	c := &Consolidator{
		DB:         db,
		Journal:    w,
		Summarizer: &stubSummarizer{Reply: "not json at all, just prose from the model"},
		Logger:     quietLogger(),
	}
	tmp := t.TempDir()
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Since:       time.Hour,
		MinEntries:  10,
		OutputDir:   tmp,
	})
	if err != nil {
		t.Fatalf("run should not error on malformed llm response: %v", err)
	}
	if res.RulesAppended != 0 {
		t.Errorf("expected 0 rules, got %d", res.RulesAppended)
	}
	// A marker journal entry should still have been written so operators
	// can see the worker ran.
	if res.JournalEntryID == "" {
		t.Errorf("expected a memory.consolidated marker even when no rules")
	}
	// No file should be created when there are no rules.
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Errorf("expected empty dir on no rules, got %d", len(entries))
	}
}

func TestConsolidator_CodeFenceWrappedResponse(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	wrapped := "Here is my analysis:\n```json\n[{\"pattern\":\"p\",\"action\":\"a\",\"evidence\":[\"" + ids[0] + "\",\"" + ids[1] + "\"],\"confidence\":0.9}]\n```\nHope that helps."
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: wrapped}, Logger: quietLogger(),
	}
	tmp := t.TempDir()
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: tmp,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RulesAppended != 1 {
		t.Errorf("expected 1 rule through code-fence wrapping, got %d", res.RulesAppended)
	}
}

func TestConsolidator_SingleEvidenceFiltered(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	// One rule with one piece of evidence should be dropped; one rule
	// with two pieces of evidence must survive.
	reply := `[
      {"pattern":"flaky","action":"retry","evidence":["` + ids[0] + `"],"confidence":0.9},
      {"pattern":"stable","action":"log","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.7}
    ]`
	c := &Consolidator{
		DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger(),
	}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.RulesAppended != 1 {
		t.Errorf("single-evidence rule not filtered: %d", res.RulesAppended)
	}
}

func TestConsolidator_KeeperDeniedOnlyFlowsThrough(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	// 10 allowed keeper decisions — should all be filtered out.
	for i := 0; i < 10; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i) * time.Minute)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
			 VALUES (?, 'ws_test', 'crew_test', ?, 'keeper.decision', 'info', 'keeper', 'keeper', 'allow', '{"decision":"allow"}', '{}')`,
			"j_allow_"+itoa3(i), ts.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert allow: %v", err)
		}
	}
	// 2 denied — also below threshold on their own, so overall skipped.
	for i := 0; i < 2; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i) * time.Minute)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
			 VALUES (?, 'ws_test', 'crew_test', ?, 'keeper.decision', 'info', 'keeper', 'keeper', 'deny', '{"decision":"deny"}', '{}')`,
			"j_deny_"+itoa3(i), ts.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("insert deny: %v", err)
		}
	}

	stub := &stubSummarizer{Reply: "[]"}
	c := &Consolidator{DB: db, Journal: w, Summarizer: stub, Logger: quietLogger()}
	res, err := c.Run(ctx, Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Only 2 denials make it through the filter; below threshold -> skipped.
	if !res.Skipped {
		t.Fatalf("expected skip after keeper filter, got %+v", res)
	}
	if res.EntriesScanned != 2 {
		t.Errorf("expected 2 denied kept, got %d", res.EntriesScanned)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("summarizer should not be called when below threshold")
	}
}

func TestConsolidator_MissingRequiredConfig(t *testing.T) {
	c := &Consolidator{DB: openDB(t), Journal: &noopEmitter{}, Summarizer: &stubSummarizer{}}
	cases := []Config{
		{CrewID: "x", OutputDir: "/tmp"},
		{WorkspaceID: "x", OutputDir: "/tmp"},
		{WorkspaceID: "x", CrewID: "x"},
	}
	for i, cfg := range cases {
		if _, err := c.Run(context.Background(), cfg); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

// noopEmitter satisfies journal.Emitter without touching a DB — useful
// for config-validation tests that fail before any emit happens.
type noopEmitter struct{}

func (n *noopEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	return "j_noop", nil
}
func (n *noopEmitter) Flush(ctx context.Context) error { return nil }

func TestParseRulesClampsConfidence(t *testing.T) {
	raw := `[
      {"pattern":"p","action":"a","evidence":["x","y"],"confidence":1.5},
      {"pattern":"q","action":"b","evidence":["x","y"],"confidence":-0.3}
    ]`
	rules := parseRules(raw)
	if len(rules) != 2 {
		t.Fatalf("parseRules: got %d want 2", len(rules))
	}
	if rules[0].Confidence != 1.0 {
		t.Errorf("clamp hi: got %v", rules[0].Confidence)
	}
	if rules[1].Confidence != 0.0 {
		t.Errorf("clamp lo: got %v", rules[1].Confidence)
	}
}

func TestParseRulesHandlesEmptyResponse(t *testing.T) {
	for _, raw := range []string{"", "   ", "[]", "```json\n[]\n```"} {
		if out := parseRules(raw); len(out) != 0 {
			t.Errorf("%q: expected empty, got %+v", raw, out)
		}
	}
}

func TestAppendRulesCreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "a", "b", "c")
	c := &Consolidator{}
	path, content, err := c.appendRules(nested, time.Date(2026, 4, 17, 10, 0, 0, 0, time.UTC),
		[]LearnedRule{{Pattern: "p", Action: "a", Evidence: []string{"x", "y"}, Confidence: 0.5}})
	if err != nil {
		t.Fatalf("appendRules: %v", err)
	}
	if !strings.HasSuffix(path, "learned-2026-04-17.md") {
		t.Errorf("unexpected path: %s", path)
	}
	if len(content) == 0 {
		t.Errorf("returned content should be the post-write file body, got empty")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}
