package consolidate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// failEmitter satisfies journal.Emitter and fails Emit after `okFor`
// successful calls. Used to drive the best-effort / error-return emit
// branches in Run without faking the whole journal.
type failEmitter struct {
	okFor int
	calls int
}

func (f *failEmitter) Emit(ctx context.Context, e journal.Entry) (string, error) {
	f.calls++
	if f.calls > f.okFor {
		return "", errors.New("emit failed")
	}
	return "j_ok", nil
}
func (f *failEmitter) Flush(ctx context.Context) error { return nil }

// seedPriorityEntry inserts one journal row with an explicit priority so
// the consolidator's priority scan picks it up.
func seedPriorityEntry(t *testing.T, db *sql.DB, id, crewID string, prio journal.Priority, summary string) {
	t.Helper()
	ts := time.Now().UTC().Add(-time.Minute)
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO journal_entries
		 (id, workspace_id, crew_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		 VALUES (?, 'ws_test', ?, ?, 'peer.escalation', 'info', ?, 'agent', 'a', ?, '{}', '{}')`,
		id, crewID, ts.Format(time.RFC3339Nano), string(prio), summary)
	if err != nil {
		t.Fatalf("seed priority entry: %v", err)
	}
}

// --- snapshotPins (direct) ---------------------------------------------------

func TestSnapshotPins_EmptyOutputDirIsNoop(t *testing.T) {
	wrote, err := snapshotPins(Config{}, []journal.Entry{{ID: "x", Priority: journal.PriorityPin}})
	if err != nil || wrote {
		t.Errorf("empty OutputDir: wrote=%v err=%v, want false/nil", wrote, err)
	}
}

func TestSnapshotPins_NoPinEntriesIsNoop(t *testing.T) {
	dir := t.TempDir()
	wrote, err := snapshotPins(Config{OutputDir: dir},
		[]journal.Entry{{ID: "x", Priority: journal.PriorityPermanent}})
	if err != nil || wrote {
		t.Errorf("no pins: wrote=%v err=%v, want false/nil", wrote, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pins.md")); !os.IsNotExist(err) {
		t.Errorf("pins.md should not exist, stat err=%v", err)
	}
}

func TestSnapshotPins_WritesAndDedupsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	entries := []journal.Entry{
		{ID: "j_pin_1", Priority: journal.PriorityPin, Type: journal.EntryPeerEscalation,
			TS: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), Summary: "never restart prod on friday"},
		{ID: "j_pin_2", Priority: journal.PriorityPin, Type: journal.EntryMissionStatus,
			TS: time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC), Summary: "rotate keys monthly"},
		{ID: "j_norm", Priority: journal.PriorityNormal, Summary: "ignored"},
	}
	wrote, err := snapshotPins(Config{OutputDir: dir}, entries)
	if err != nil {
		t.Fatalf("snapshotPins: %v", err)
	}
	if !wrote {
		t.Fatal("first call must report wrote=true")
	}
	body, err := os.ReadFile(filepath.Join(dir, "pins.md"))
	if err != nil {
		t.Fatalf("read pins.md: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"# Pinned entries", // first-write header
		"j_pin_1", "never restart prod on friday", "2026-06-01",
		"j_pin_2", "rotate keys monthly",
		"<!-- pin-id:j_pin_1 -->",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pins.md missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "j_norm") {
		t.Errorf("non-pin entry leaked into pins.md:\n%s", s)
	}

	// Rerun on identical input: dedup via the pin-id comment markers →
	// wrote=false, file byte-identical.
	wrote2, err := snapshotPins(Config{OutputDir: dir}, entries)
	if err != nil {
		t.Fatalf("second snapshotPins: %v", err)
	}
	if wrote2 {
		t.Error("rerun with same pin IDs must report wrote=false")
	}
	body2, _ := os.ReadFile(filepath.Join(dir, "pins.md"))
	if string(body2) != s {
		t.Errorf("pins.md changed on dedup rerun:\n--- before ---\n%s\n--- after ---\n%s", s, body2)
	}

	// A new pin appends without re-writing the header.
	wrote3, err := snapshotPins(Config{OutputDir: dir}, []journal.Entry{
		{ID: "j_pin_3", Priority: journal.PriorityPin, TS: time.Now().UTC(), Summary: "third pin"},
	})
	if err != nil || !wrote3 {
		t.Fatalf("third call: wrote=%v err=%v", wrote3, err)
	}
	body3, _ := os.ReadFile(filepath.Join(dir, "pins.md"))
	if got := strings.Count(string(body3), "# Pinned entries"); got != 1 {
		t.Errorf("header must appear exactly once, got %d", got)
	}
	if !strings.Contains(string(body3), "j_pin_3") {
		t.Errorf("new pin not appended:\n%s", body3)
	}
}

// --- Run: priority + no-summarizer paths --------------------------------------

// memoryVersionsSchema is the minimal v90 surface recordCanonicalVersion
// needs (same shape the approve tests use).
const memoryVersionsSchema = `
CREATE TABLE IF NOT EXISTS memory_versions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);`

func TestConsolidator_PermanentBypassesThreshold_NoSummarizer(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(memoryVersionsSchema); err != nil {
		t.Fatalf("v90 schema: %v", err)
	}
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// Only 2 entries (below MinEntries=10) but one is permanent and one
	// is pinned: the permanent marker bypasses the skip, the pin lands
	// in pins.md, and the nil Summarizer must NOT panic (issue #543).
	seedPriorityEntry(t, db, "j_perm_1", "crew_test", journal.PriorityPermanent, "remember forever")
	seedPriorityEntry(t, db, "j_pin_a", "crew_test", journal.PriorityPin, "pin me please")

	outDir := t.TempDir()
	blobRoot := t.TempDir()
	c := &Consolidator{DB: db, Journal: w, Summarizer: nil, Logger: quietLogger()}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: outDir, BlobRoot: blobRoot,
	})
	if err != nil {
		t.Fatalf("Run with nil summarizer: %v", err)
	}
	if res.Skipped {
		t.Errorf("permanent entry must bypass the below-threshold skip: %+v", res)
	}
	if res.RulesAppended != 0 {
		t.Errorf("no summarizer → no rules, got %d", res.RulesAppended)
	}

	// pins.md captured the pin.
	body, err := os.ReadFile(filepath.Join(outDir, "pins.md"))
	if err != nil {
		t.Fatalf("pins.md missing: %v", err)
	}
	if !strings.Contains(string(body), "pin me please") || !strings.Contains(string(body), "j_pin_a") {
		t.Errorf("pins.md content wrong:\n%s", body)
	}

	// Versioning hook fired for the pins write: a memory_versions row
	// with tier=pins under the crew-scoped audit path.
	var tier, path, writtenBy string
	if err := db.QueryRow(
		`SELECT tier, path, COALESCE(written_by,'') FROM memory_versions WHERE workspace_id = 'ws_test'`,
	).Scan(&tier, &path, &writtenBy); err != nil {
		t.Fatalf("memory_versions row missing: %v", err)
	}
	if tier != "pins" || path != "crew:crew_test/pins.md" || writtenBy != "consolidator" {
		t.Errorf("version row = (%s, %s, %s), want (pins, crew:crew_test/pins.md, consolidator)", tier, path, writtenBy)
	}
}

func TestConsolidator_PinOnlyBelowThresholdStillSkips(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// pin (not permanent) does NOT bypass the threshold, but the pin
	// snapshot still runs before the skip decision.
	seedPriorityEntry(t, db, "j_pin_only", "crew_test", journal.PriorityPin, "pinned but below threshold")

	outDir := t.TempDir()
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: "[]"}, Logger: quietLogger()}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: outDir,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Skipped {
		t.Errorf("pin alone must not bypass MinEntries: %+v", res)
	}
	body, err := os.ReadFile(filepath.Join(outDir, "pins.md"))
	if err != nil {
		t.Fatalf("pins.md should be written even on skip: %v", err)
	}
	if !strings.Contains(string(body), "j_pin_only") {
		t.Errorf("pins.md missing pinned entry:\n%s", body)
	}
}

// --- Run: error branches --------------------------------------------------------

func TestConsolidator_SummarizeErrorPropagates(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)
	c := &Consolidator{
		DB: db, Journal: w, Logger: quietLogger(),
		Summarizer: &stubSummarizer{ReplyFn: func(string) (string, error) {
			return "", errors.New("ollama down")
		}},
	}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "consolidate: summarize") {
		t.Fatalf("expected wrapped summarize error, got %v", err)
	}
	if res.EntriesScanned != 12 {
		t.Errorf("EntriesScanned should survive the error, got %d", res.EntriesScanned)
	}
}

func TestConsolidator_EmitErrorOnZeroRules(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)

	c := &Consolidator{
		DB: db, Journal: &failEmitter{okFor: 0}, Logger: quietLogger(),
		Summarizer: &stubSummarizer{Reply: "[]"},
	}
	_, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "consolidate: emit empty") {
		t.Errorf("expected 'emit empty' error, got %v", err)
	}
}

func TestConsolidator_EmitErrorAfterRulesWritten(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	ids := seedEntries(t, db, w, "ws_test", "crew_test", 12, journal.EntryPeerEscalation)

	reply := `[{"pattern":"p1","action":"a1","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.9}]`
	outDir := t.TempDir()
	c := &Consolidator{
		DB: db, Journal: &failEmitter{okFor: 0}, Logger: quietLogger(),
		Summarizer: &stubSummarizer{Reply: reply},
	}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10, OutputDir: outDir,
	})
	if err == nil || !strings.Contains(err.Error(), "consolidate: emit:") {
		t.Fatalf("expected 'consolidate: emit' error, got %v", err)
	}
	// Partial result still reports the on-disk write that DID happen.
	if res.RulesAppended != 1 || res.OutputPath == "" {
		t.Errorf("partial result should carry written state: %+v", res)
	}
	if _, statErr := os.Stat(res.OutputPath); statErr != nil {
		t.Errorf("learned file should exist despite emit failure: %v", statErr)
	}
}

func TestConsolidator_ListErrorPropagates(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE journal_entries`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	c := &Consolidator{DB: db, Journal: &noopEmitter{}, Summarizer: &stubSummarizer{}, Logger: quietLogger()}
	_, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "consolidate: list") {
		t.Errorf("expected 'consolidate: list' error, got %v", err)
	}
}

// --- recordCanonicalVersion / logger -------------------------------------------

func TestRecordCanonicalVersion_GatesAndReadFailure(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(memoryVersionsSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	ctx := context.Background()

	countRows := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// Gate 1: nil DB → silent no-op (must not panic).
	(&Consolidator{Logger: quietLogger()}).recordCanonicalVersion(ctx,
		Config{BlobRoot: t.TempDir()}, "/nonexistent", memory.TierPins, "x")

	c := &Consolidator{DB: db, Logger: quietLogger()}

	// Gate 2: empty BlobRoot → no row.
	c.recordCanonicalVersion(ctx, Config{WorkspaceID: "ws_test"}, "/nonexistent", memory.TierPins, "x")
	if n := countRows(); n != 0 {
		t.Errorf("BlobRoot gate failed: %d rows", n)
	}

	// Read failure: missing canonical file → warn, no row, no panic.
	c.recordCanonicalVersion(ctx,
		Config{WorkspaceID: "ws_test", BlobRoot: t.TempDir()},
		filepath.Join(t.TempDir(), "does-not-exist.md"), memory.TierPins, "crew:c/pins.md")
	if n := countRows(); n != 0 {
		t.Errorf("read-failure path inserted a row: %d", n)
	}

	// Success: real file → exactly one row with the audit path.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.md")
	if err := os.WriteFile(path, []byte("pinned content\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.recordCanonicalVersion(ctx,
		Config{WorkspaceID: "ws_test", BlobRoot: t.TempDir()},
		path, memory.TierPins, "crew:crew_test/pins.md")
	if n := countRows(); n != 1 {
		t.Fatalf("expected 1 version row, got %d", n)
	}
	var auditPath string
	if err := db.QueryRow(`SELECT path FROM memory_versions`).Scan(&auditPath); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if auditPath != "crew:crew_test/pins.md" {
		t.Errorf("audit path = %q", auditPath)
	}
}

func TestRecordCanonicalVersionContent_InsertFailureIsBestEffort(t *testing.T) {
	db := openDB(t) // no memory_versions table at all
	defer db.Close()
	c := &Consolidator{DB: db, Logger: quietLogger()}
	// Must warn-and-return, never panic or propagate.
	c.recordCanonicalVersionContent(context.Background(),
		Config{WorkspaceID: "ws_test", BlobRoot: t.TempDir()},
		[]byte("content"), memory.TierLearned, "crew:c/learned-x.md")
}

func TestConsolidatorLoggerFallback(t *testing.T) {
	c := &Consolidator{}
	if c.logger() == nil {
		t.Error("nil Logger must fall back to slog.Default, got nil")
	}
	lg := quietLogger()
	c.Logger = lg
	if c.logger() != lg {
		t.Error("explicit Logger not returned")
	}
}

// --- misc branch top-ups ---------------------------------------------------------

func TestSnapshotPins_MkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	_, err := snapshotPins(Config{OutputDir: filepath.Join(blocker, "sub")},
		[]journal.Entry{{ID: "p1", Priority: journal.PriorityPin, TS: time.Now()}})
	if err == nil || !strings.Contains(err.Error(), "pins mkdir") {
		t.Errorf("expected pins mkdir error, got %v", err)
	}
}

// TestConsolidator_PinsSnapshotFailureIsNonFatal: a broken OutputDir makes
// snapshotPins fail; Run must warn-and-continue into the normal skip path
// rather than aborting the tick.
func TestConsolidator_PinsSnapshotFailureIsNonFatal(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	seedPriorityEntry(t, db, "j_pin_bad", "crew_test", journal.PriorityPin, "pin into broken dir")

	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: "[]"}, Logger: quietLogger()}
	res, err := c.Run(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test",
		Since: time.Hour, MinEntries: 10,
		OutputDir: filepath.Join(blocker, "topics"),
	})
	if err != nil {
		t.Fatalf("pins failure must not abort Run: %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected skip after pin failure (below threshold): %+v", res)
	}
}

func TestAppendRules_MkdirFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	c := &Consolidator{}
	_, _, err := c.appendRules(filepath.Join(blocker, "topics"), time.Now(),
		[]LearnedRule{{Pattern: "p", Action: "a"}})
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected mkdir error, got %v", err)
	}
}

func TestAppendRules_LockFailure(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	// A directory squatting on the sentinel path makes flock's
	// OpenFile fail with EISDIR.
	if err := os.Mkdir(filepath.Join(dir, "learned-2026-06-12.md.lock"), 0o755); err != nil {
		t.Fatalf("mkdir lock blocker: %v", err)
	}
	c := &Consolidator{}
	_, _, err := c.appendRules(dir, now, []LearnedRule{{Pattern: "p", Action: "a"}})
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("expected lock error, got %v", err)
	}
}

func TestCollectEvidence_DeduplicatesAcrossRules(t *testing.T) {
	got := collectEvidence([]LearnedRule{
		{Evidence: []string{"a", "b"}},
		{Evidence: []string{"b", "c", "a"}},
	})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 unique ids: %v", len(got), got)
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order not preserved: got %v, want %v", got, want)
			break
		}
	}
}

func TestParseRules_DropsFullyBlankRules(t *testing.T) {
	rules := parseRules(`[{"pattern":"  ","action":"","evidence":["a","b"],"confidence":0.5},
	                      {"pattern":"keep","action":"me","evidence":["a","b"],"confidence":0.5}]`)
	if len(rules) != 1 || rules[0].Pattern != "keep" {
		t.Errorf("blank rule should be dropped, got %+v", rules)
	}
}

func TestParseRules_NoArrayInResponse(t *testing.T) {
	for _, raw := range []string{"{}", "no brackets here", "]["} {
		if out := parseRules(raw); len(out) != 0 {
			t.Errorf("%q: expected nil, got %+v", raw, out)
		}
	}
}

// --- isDenied -------------------------------------------------------------------

func TestIsDenied_Table(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"nil payload", nil, false},
		{"decision deny", map[string]any{"decision": "deny"}, true},
		{"decision denied uppercase", map[string]any{"decision": " DENIED "}, true},
		{"outcome reject", map[string]any{"outcome": "reject"}, true},
		{"result rejected", map[string]any{"result": "rejected"}, true},
		{"decision allow", map[string]any{"decision": "allow"}, false},
		{"non-string decision", map[string]any{"decision": 42}, false},
		{"allowed false", map[string]any{"allowed": false}, true},
		{"allowed true", map[string]any{"allowed": true}, false},
		{"allowed non-bool", map[string]any{"allowed": "no"}, false},
		{"unrelated keys", map[string]any{"foo": "bar"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDenied(tc.payload); got != tc.want {
				t.Errorf("isDenied(%v) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}
