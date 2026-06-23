package consolidate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// archivedSchema is the v-archive table the compactor copies rows into
// before deleting. Matches the columns archiveBucket INSERTs.
const archivedSchema = `
CREATE TABLE journal_entries_archived (
	id TEXT PRIMARY KEY, workspace_id TEXT, crew_id TEXT, agent_id TEXT, mission_id TEXT,
	ts TEXT, archived_at TEXT, entry_type TEXT, severity TEXT, priority TEXT DEFAULT 'normal',
	actor_type TEXT, actor_id TEXT, summary TEXT, compressed_payload TEXT, original_size_bytes INTEGER);`

// --- now / parseTS ---------------------------------------------------------------

func TestCompactorNow_InjectedClock(t *testing.T) {
	pinned := time.Date(2026, 6, 1, 15, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	c := &Compactor{Now: func() time.Time { return pinned }}
	got := c.now()
	if !got.Equal(pinned) {
		t.Errorf("now() = %v, want pinned instant", got)
	}
	if got.Location() != time.UTC {
		t.Errorf("now() must normalise to UTC, got %v", got.Location())
	}

	// Nil clock falls back to wall time (sanity-bounded, not exact).
	before := time.Now().UTC().Add(-time.Minute)
	wall := (&Compactor{}).now()
	if wall.Before(before) || wall.After(time.Now().UTC().Add(time.Minute)) {
		t.Errorf("fallback now() implausible: %v", wall)
	}
}

func TestParseTS_AcceptedLayoutsAndFailure(t *testing.T) {
	want := time.Date(2026, 5, 4, 10, 20, 30, 0, time.UTC)
	cases := []string{
		"2026-05-04T10:20:30.000Z",
		"2026-05-04T10:20:30Z",
		"2026-05-04 10:20:30",
		"2026-05-04T10:20:30+00:00",
	}
	for _, in := range cases {
		got, err := parseTS(in)
		if err != nil {
			t.Errorf("parseTS(%q): %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseTS(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseTS("yesterday-ish"); err == nil {
		t.Error("expected error for unparseable timestamp")
	}
}

// --- Run defaults + archive integration --------------------------------------------

func TestCompactor_DefaultOlderThanAndNilLogger(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	// 12 chunks aged 20 days — younger than the 30-day default cutoff
	// that kicks in when olderThan <= 0 is passed.
	old := time.Now().UTC().Add(-20 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("def", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
	}
	c := &Compactor{DB: db, Journal: w} // nil Logger → slog.Default branch
	res, err := c.Run(context.Background(), "ws_test", 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.EntriesDeleted != 0 || res.BucketsCreated != 0 {
		t.Errorf("20-day-old entries must survive the 30-day default cutoff: %+v", res)
	}
}

func TestCompactor_ArchivesBeforeDelete(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(archivedSchema); err != nil {
		t.Fatalf("archive schema: %v", err)
	}
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	longPayload := `{"line":"` + strings.Repeat("x", 600) + `"}`
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("arch", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "big output", longPayload)
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.EntriesArchived != 12 {
		t.Errorf("EntriesArchived = %d, want 12", res.EntriesArchived)
	}
	if res.EntriesDeleted != 12 {
		t.Errorf("EntriesDeleted = %d, want 12", res.EntriesDeleted)
	}

	// Archived rows carry a truncated payload (400-char cap) and the
	// original size for accounting.
	var n int
	var maxPayloadLen, minOrigSize int64
	if err := db.QueryRow(
		`SELECT COUNT(*), MAX(length(compressed_payload)), MIN(original_size_bytes)
		   FROM journal_entries_archived WHERE workspace_id = 'ws_test'`,
	).Scan(&n, &maxPayloadLen, &minOrigSize); err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if n != 12 {
		t.Errorf("archive rows = %d, want 12", n)
	}
	if maxPayloadLen > 400 {
		t.Errorf("compressed_payload exceeds 400-char cap: %d", maxPayloadLen)
	}
	if minOrigSize <= 400 {
		t.Errorf("original_size_bytes should reflect the full pre-truncation size, got %d", minOrigSize)
	}
}

// TestCompactor_SkipsUnparseableTimestamps: a corrupt ts row that sorts
// below the cutoff string-wise is selected but dropped at parse time —
// the run continues and the corrupt row survives untouched.
func TestCompactor_SkipsUnparseableTimestamps(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("ts", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
	}
	// Corrupt ts: '0000garbage' sorts lexically below any RFC3339 cutoff
	// so the SELECT picks it up; parseTS then rejects it.
	if _, err := db.Exec(
		`INSERT INTO journal_entries (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
		 VALUES ('j_corrupt', 'ws_test', 'crew_test', '0000garbage', 'exec.output_chunk', 'info', 'system', 't', 's', '{}', '{}')`); err != nil {
		t.Fatalf("insert corrupt: %v", err)
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.EntriesDeleted != 12 {
		t.Errorf("deleted = %d, want 12 (corrupt row excluded)", res.EntriesDeleted)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id = 'j_corrupt'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("corrupt-ts row must survive compaction, got %d", n)
	}
}

// --- Run failure branches ------------------------------------------------------------

// flushFailEmitter emits fine but fails Flush — exercises the "flush
// after emit failed" continue branch in Run.
type flushFailEmitter struct{ noopEmitter }

func (f *flushFailEmitter) Flush(ctx context.Context) error {
	return context.DeadlineExceeded
}

func TestCompactor_EmitBucketFailureSkipsBucket(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("ef", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
	}
	c := &Compactor{DB: db, Journal: &failEmitter{okFor: 0}, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Run must not fail outright on bucket-emit error: %v", err)
	}
	if res.BucketsCreated != 0 || res.EntriesDeleted != 0 {
		t.Errorf("failed emit must skip the bucket entirely: %+v", res)
	}
	// Originals are untouched — the delete never ran.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='exec.output_chunk'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 12 {
		t.Errorf("originals = %d, want 12 untouched", n)
	}
}

func TestCompactor_FlushFailureSkipsBucket(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("ff", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
	}
	c := &Compactor{DB: db, Journal: &flushFailEmitter{}, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.EntriesDeleted != 0 {
		t.Errorf("flush failure must prevent deletion, got %+v", res)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='exec.output_chunk'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 12 {
		t.Errorf("originals = %d, want 12 (delete must not run before durable summary)", n)
	}
}

func TestCompactor_RunMarkerEmitFailureSurfaces(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("rm", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
	}
	// First Emit (bucket summary) succeeds, second (run marker) fails.
	c := &Compactor{DB: db, Journal: &failEmitter{okFor: 1}, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err == nil || !strings.Contains(err.Error(), "emit run marker") {
		t.Fatalf("expected run-marker emit error, got %v", err)
	}
	// The work itself happened before the marker — partial result is real.
	if res.BucketsCreated != 1 || res.EntriesDeleted != 12 {
		t.Errorf("partial result should reflect completed work: %+v", res)
	}
}

func TestCompactor_SelectError(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE journal_entries`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	c := &Compactor{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err == nil || !strings.Contains(err.Error(), "compact: select") {
		t.Errorf("expected select error, got %v", err)
	}
}

// --- direct helper error branches -----------------------------------------------------

func TestDeleteBucket_BeginError(t *testing.T) {
	db := openDB(t)
	db.Close()
	c := &Compactor{DB: db}
	_, _, err := c.deleteBucket(context.Background(), "ws_test", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "begin") {
		t.Errorf("expected begin error, got %v", err)
	}
}

func TestArchiveBucket_EmptyIDsAndBeginError(t *testing.T) {
	c := &Compactor{}
	n, err := c.archiveBucket(context.Background(), "ws_test", nil)
	if n != 0 || err != nil {
		t.Errorf("empty ids: n=%d err=%v, want 0/nil", n, err)
	}

	db := openDB(t)
	db.Close()
	c2 := &Compactor{DB: db}
	if _, err := c2.archiveBucket(context.Background(), "ws_test", []string{"a"}); err == nil ||
		!strings.Contains(err.Error(), "archive begin") {
		t.Errorf("expected archive begin error, got %v", err)
	}
}

func TestEmitBucketSummary_BadDate(t *testing.T) {
	c := &Compactor{Journal: &noopEmitter{}}
	_, err := c.emitBucketSummary(context.Background(), "ws_test", bucket{Date: "not-a-date"})
	if err == nil || !strings.Contains(err.Error(), "bad bucket date") {
		t.Errorf("expected bad-date error, got %v", err)
	}
}
