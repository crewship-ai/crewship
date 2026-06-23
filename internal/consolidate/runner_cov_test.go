package consolidate

import (
	"bytes"
	"context"
	"errors"
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

// --- listCrews / listWorkspaces ---------------------------------------------

func TestListCrews_FiltersDeleted(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()

	// Second live crew + one soft-deleted crew. The seed schema already
	// holds (crew_test, ws_test, crew-test).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, slug) VALUES ('crew_b', 'ws_test', 'crew-b')`); err != nil {
		t.Fatalf("insert crew_b: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, slug, deleted_at) VALUES ('crew_gone', 'ws_test', 'crew-gone', '2026-01-01')`); err != nil {
		t.Fatalf("insert crew_gone: %v", err)
	}

	crews, err := listCrews(ctx, db)
	if err != nil {
		t.Fatalf("listCrews: %v", err)
	}
	if len(crews) != 2 {
		t.Fatalf("listCrews len = %d, want 2 (deleted crews must be filtered)", len(crews))
	}
	byID := map[string]crewRow{}
	for _, c := range crews {
		byID[c.ID] = c
	}
	if _, ok := byID["crew_gone"]; ok {
		t.Errorf("deleted crew leaked through listCrews")
	}
	got, ok := byID["crew_test"]
	if !ok {
		t.Fatalf("crew_test missing from %v", crews)
	}
	if got.WorkspaceID != "ws_test" || got.Slug != "crew-test" {
		t.Errorf("crew_test row = %+v, want ws_test/crew-test", got)
	}
}

func TestListCrews_QueryError(t *testing.T) {
	db := openDB(t)
	db.Close() // closed handle → QueryContext fails
	if _, err := listCrews(context.Background(), db); err == nil {
		t.Error("expected error from listCrews on closed DB, got nil")
	}
}

func TestListWorkspaces_ReturnsAll(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id) VALUES ('ws_two')`); err != nil {
		t.Fatalf("insert ws_two: %v", err)
	}
	ws, err := listWorkspaces(ctx, db)
	if err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	if len(ws) != 2 {
		t.Fatalf("len = %d, want 2", len(ws))
	}
	found := map[string]bool{}
	for _, id := range ws {
		found[id] = true
	}
	if !found["ws_test"] || !found["ws_two"] {
		t.Errorf("workspaces = %v, want ws_test + ws_two", ws)
	}
}

func TestListWorkspaces_QueryError(t *testing.T) {
	db := openDB(t)
	db.Close()
	if _, err := listWorkspaces(context.Background(), db); err == nil {
		t.Error("expected error from listWorkspaces on closed DB, got nil")
	}
}

// --- hitlEnabled ------------------------------------------------------------

func TestHitlEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{"  on  ", true}, // whitespace trimmed
		{"", false},
		{"0", false},
		{"off", false},
		{"banana", false},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv("CREWSHIP_CONSOLIDATE_HITL", tc.val)
			if got := hitlEnabled(); got != tc.want {
				t.Errorf("hitlEnabled() with %q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// --- consolidateAllCrews ------------------------------------------------------

func TestConsolidateAllCrews_HappyPath(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "") // direct-write contract
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"runner pattern","action":"runner action","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.9}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}

	root := t.TempDir()
	err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{
		CrewMemoryRoot:     root,
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	}))
	if err != nil {
		t.Fatalf("consolidateAllCrews: %v", err)
	}

	// The output dir is derived from CrewMemoryRoot + crew slug + topics.
	outDir := filepath.Join(root, "crew-test", "topics")
	entries, derr := os.ReadDir(outDir)
	if derr != nil {
		t.Fatalf("expected learned output dir %s: %v", outDir, derr)
	}
	var learned string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "learned-") && strings.HasSuffix(e.Name(), ".md") {
			learned = filepath.Join(outDir, e.Name())
		}
	}
	if learned == "" {
		t.Fatalf("no learned-*.md written under %s", outDir)
	}
	body, _ := os.ReadFile(learned)
	if !strings.Contains(string(body), "runner pattern") {
		t.Errorf("learned file missing rule pattern:\n%s", body)
	}
}

func TestConsolidateAllCrews_PerCrewErrorAggregated(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	c := &Consolidator{
		DB: db, Journal: w, Logger: quietLogger(),
		Summarizer: &stubSummarizer{ReplyFn: func(string) (string, error) {
			return "", errors.New("llm exploded")
		}},
	}
	err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{
		CrewMemoryRoot:     t.TempDir(),
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	}))
	if err == nil {
		t.Fatal("expected aggregated error when a crew's summarizer fails")
	}
	if !strings.Contains(err.Error(), "crew crew_test") || !strings.Contains(err.Error(), "llm exploded") {
		t.Errorf("error should name the crew and wrap the cause, got: %v", err)
	}
}

func TestConsolidateAllCrews_ListCrewsError(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE crews`); err != nil {
		t.Fatalf("drop crews: %v", err)
	}
	c := &Consolidator{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	err := consolidateAllCrews(context.Background(), db, c, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err == nil || !strings.Contains(err.Error(), "list crews") {
		t.Errorf("expected 'list crews' error, got %v", err)
	}
}

func TestConsolidateAllCrews_CtxCancelledMidLoop(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// Two crews, each above MinEntries, so whichever the loop hits first
	// invokes the summarizer; the summarizer cancels the context, and the
	// loop must bail out with ctx.Err() before reaching the second crew.
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug) VALUES ('crew_b', 'ws_test', 'crew-b')`); err != nil {
		t.Fatalf("insert crew_b: %v", err)
	}
	seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	for i := 0; i < 12; i++ {
		ts := time.Now().UTC().Add(-time.Duration(i) * time.Minute)
		if _, err := db.Exec(
			`INSERT INTO journal_entries (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
			 VALUES (?, 'ws_test', 'crew_b', ?, 'peer.escalation', 'info', 'agent', 'a', 's', '{}', '{}')`,
			"j_b_"+itoa3(i), ts.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed crew_b: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Consolidator{
		DB: db, Journal: w, Logger: quietLogger(),
		Summarizer: &stubSummarizer{ReplyFn: func(string) (string, error) {
			cancel()
			return "[]", nil
		}},
	}
	err := consolidateAllCrews(ctx, db, c, applyDefaults(RunnerOptions{
		CrewMemoryRoot:     t.TempDir(),
		ConsolidationSince: time.Hour,
		Logger:             quietLogger(),
	}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled after mid-loop cancel, got %v", err)
	}
}

// --- compactAllWorkspaces -----------------------------------------------------

func TestCompactAllWorkspaces_HappyPath(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// A second workspace with NO aged entries exercises the
	// zero-buckets continue branch in the same pass.
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws_idle')`); err != nil {
		t.Fatalf("insert ws_idle: %v", err)
	}

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("rcav", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "out", `{"line":"x"}`)
	}
	comp := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	err := compactAllWorkspaces(context.Background(), db, comp, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err != nil {
		t.Fatalf("compactAllWorkspaces: %v", err)
	}
	var remaining int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE entry_type = 'exec.output_chunk'`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("aged chunks should be compacted away, %d remain", remaining)
	}
}

func TestCompactAllWorkspaces_PerWorkspaceErrorAggregated(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	// journal_entries gone → Compactor.Run fails for ws_test but the
	// loop still aggregates rather than panicking.
	if _, err := db.Exec(`DROP TABLE journal_entries`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	comp := &Compactor{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	err := compactAllWorkspaces(context.Background(), db, comp, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err == nil || !strings.Contains(err.Error(), "workspace ws_test") {
		t.Errorf("expected aggregated per-workspace error, got %v", err)
	}
}

func TestCompactAllWorkspaces_ListError(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE workspaces`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	comp := &Compactor{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	err := compactAllWorkspaces(context.Background(), db, comp, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err == nil || !strings.Contains(err.Error(), "list workspaces") {
		t.Errorf("expected 'list workspaces' error, got %v", err)
	}
}

// --- snapshotAllWorkspaces ----------------------------------------------------

// healthExtras adds the tables ComputeHealth/PersistSnapshot need on top
// of the base openDB schema (which already has workspaces + journal_entries).
const healthExtras = `
CREATE TABLE journal_embeddings (
	entry_id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT,
	model TEXT, dim INTEGER, vector BLOB, indexed_at TEXT,
	importance_score REAL DEFAULT 0.5, reference_count INTEGER DEFAULT 0,
	last_referenced_at TEXT);
CREATE TABLE memory_relations (
	entry_id TEXT, related_entry_id TEXT, relation_kind TEXT,
	score REAL DEFAULT 0, created_at TEXT DEFAULT (datetime('now')),
	PRIMARY KEY(entry_id, related_entry_id, relation_kind));
CREATE TABLE journal_entries_archived (
	id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT, mission_id TEXT,
	ts TEXT, archived_at TEXT, entry_type TEXT, severity TEXT, priority TEXT DEFAULT 'normal',
	actor_type TEXT, actor_id TEXT, summary TEXT, compressed_payload TEXT, original_size_bytes INTEGER);
CREATE TABLE memory_health_snapshots (
	id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, computed_at TEXT,
	freshness REAL, coverage REAL, coherence REAL, efficiency REAL, reachability REAL, overall REAL, details TEXT);
`

func TestSnapshotAllWorkspaces_PersistsSnapshot(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(healthExtras); err != nil {
		t.Fatalf("health schema: %v", err)
	}
	err := snapshotAllWorkspaces(context.Background(), db, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err != nil {
		t.Fatalf("snapshotAllWorkspaces: %v", err)
	}
	var n int
	var ws string
	if err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(workspace_id),'') FROM memory_health_snapshots`).Scan(&n, &ws); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if n != 1 || ws != "ws_test" {
		t.Errorf("snapshots = %d for %q, want exactly 1 for ws_test", n, ws)
	}
}

func TestSnapshotAllWorkspaces_PersistFailureIsNonFatal(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(healthExtras); err != nil {
		t.Fatalf("health schema: %v", err)
	}
	// Compute succeeds, persist fails: snapshots table dropped.
	if _, err := db.Exec(`DROP TABLE memory_health_snapshots`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	err := snapshotAllWorkspaces(context.Background(), db, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err != nil {
		t.Errorf("persist failures must be swallowed (warn-and-continue), got %v", err)
	}
}

func TestSnapshotAllWorkspaces_ComputeFailureIsNonFatal(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	// No health tables at all → ComputeHealth errors per workspace but
	// the sweep itself must return nil (warn-and-continue contract).
	err := snapshotAllWorkspaces(context.Background(), db, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err != nil {
		t.Errorf("compute failures must be swallowed, got %v", err)
	}
}

func TestSnapshotAllWorkspaces_ListError(t *testing.T) {
	db := openDB(t)
	db.Close()
	err := snapshotAllWorkspaces(context.Background(), db, applyDefaults(RunnerOptions{Logger: quietLogger()}))
	if err == nil || !strings.Contains(err.Error(), "list workspaces") {
		t.Errorf("expected 'list workspaces' error, got %v", err)
	}
}

// --- StartBackground ----------------------------------------------------------

// TestStartBackground_ConsolidationTickProducesOutput drives the real
// background goroutines with a 20ms tick and waits for the consolidation
// loop to write the learned-*.md file, proving the wiring from
// StartBackground → runConsolidationLoop → consolidateAllCrews →
// Consolidator.Run is intact. The compaction loop is started too but
// targets the next daily 03:00 UTC; the returned cancel must stop both.
func TestStartBackground_ConsolidationTickProducesOutput(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"bg pattern","action":"bg action","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.9}]`

	root := t.TempDir()
	cancel := StartBackground(context.Background(), db, w, &stubSummarizer{Reply: reply}, RunnerOptions{
		ConsolidationInterval: 20 * time.Millisecond,
		ConsolidationSince:    time.Hour,
		CrewMemoryRoot:        root,
		Logger:                quietLogger(),
	})

	outDir := filepath.Join(root, "crew-test", "topics")
	deadline := time.Now().Add(5 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(outDir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "learned-") && strings.HasSuffix(e.Name(), ".md") {
				found = filepath.Join(outDir, e.Name())
			}
		}
		if found != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Cancel must stop both goroutines and return (wg.Wait inside).
	done := make(chan struct{})
	go func() {
		cancel()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel() did not return — background goroutines leaked")
	}

	if found == "" {
		t.Fatalf("consolidation tick never produced a learned-*.md under %s", outDir)
	}
	body, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read %s: %v", found, err)
	}
	if !strings.Contains(string(body), "bg pattern") {
		t.Errorf("learned file missing rule from background tick:\n%s", body)
	}
}

// TestStartBackground_CancelStopsIdleLoops covers the shutdown path when
// no tick ever fired: both loops are blocked in their selects and must
// exit promptly on cancel.
func TestStartBackground_CancelStopsIdleLoops(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	cancel := StartBackground(context.Background(), db, w, nil, RunnerOptions{
		ConsolidationInterval: time.Hour, // never fires inside the test
		CrewMemoryRoot:        t.TempDir(),
		Logger:                quietLogger(),
	})
	done := make(chan struct{})
	go func() {
		cancel()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel() hung on idle loops")
	}
}

// TestStartBackground_ConsolidationTickLogsErrors: a broken crews table
// makes every tick fail; the loop must log the warn and keep ticking
// rather than crashing the goroutine.
func TestStartBackground_ConsolidationTickLogsErrors(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE crews`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cancel := StartBackground(context.Background(), db, w, nil, RunnerOptions{
		ConsolidationInterval: 15 * time.Millisecond,
		CrewMemoryRoot:        t.TempDir(),
		Logger:                logger,
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "consolidation tick completed with errors") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if !strings.Contains(buf.String(), "consolidation tick completed with errors") {
		t.Errorf("expected warn log from failing tick, got:\n%s", buf.String())
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer so the background logger
// goroutine and the polling test goroutine don't race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// --- applyDefaults edge cases not pinned elsewhere -----------------------------

func TestApplyDefaults_MemoryVersionsKnobs(t *testing.T) {
	// Negative retention is the documented "disable" spelling — clamps to 0.
	got := applyDefaults(RunnerOptions{MemoryVersionsRetention: -1, MemoryVersionsKeepLatest: -5})
	if got.MemoryVersionsRetention != 0 {
		t.Errorf("negative retention should clamp to 0, got %v", got.MemoryVersionsRetention)
	}
	if got.MemoryVersionsKeepLatest != 0 {
		t.Errorf("negative keep-latest should clamp to 0, got %d", got.MemoryVersionsKeepLatest)
	}
	// Explicit positive values survive.
	got = applyDefaults(RunnerOptions{MemoryVersionsRetention: 7 * 24 * time.Hour, MemoryVersionsKeepLatest: 9})
	if got.MemoryVersionsRetention != 7*24*time.Hour {
		t.Errorf("explicit retention overwritten: %v", got.MemoryVersionsRetention)
	}
	if got.MemoryVersionsKeepLatest != 9 {
		t.Errorf("explicit keep-latest overwritten: %d", got.MemoryVersionsKeepLatest)
	}
}
