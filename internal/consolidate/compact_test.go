package consolidate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/journal"
)

// emitDirect inserts rows bypassing journal.Emitter so we can place the
// `ts` arbitrarily far in the past — the Emitter unconditionally stamps
// `time.Now()` which makes it useless for "older than N days" scenarios.
// This helper intentionally mirrors the exact column list from the
// migration so a schema drift shows up as a test failure rather than a
// production panic.
func emitDirect(t *testing.T, db *sql.DB, id, workspaceID, crewID string, ts time.Time, kind journal.EntryType, summary string, payload string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO journal_entries
		 (id, workspace_id, crew_id, ts, entry_type, severity, actor_type, actor_id, summary, payload, refs)
		 VALUES (?, ?, ?, ?, ?, 'info', 'system', 'test', ?, ?, '{}')`,
		id, workspaceID, crewID, ts.UTC().Format(time.RFC3339Nano), string(kind), summary, payload,
	)
	if err != nil {
		t.Fatalf("direct insert: %v", err)
	}
}

func TestCompactor_RollsUpDailyBuckets(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	old := now.Add(-45 * 24 * time.Hour) // well past the 30-day cutoff

	// 50 exec.output_chunk entries spread across 3 consecutive days,
	// sized 20/15/15 so each bucket crosses the minBucketSize=10
	// threshold. IDs are stable and synthesized by loop index so the
	// test can assert exact row counts post-delete.
	days := []time.Time{old, old.Add(24 * time.Hour), old.Add(48 * time.Hour)}
	counts := []int{20, 15, 15}
	total := 0
	for di, day := range days {
		for i := 0; i < counts[di]; i++ {
			id := makeID("chunk", di, i)
			emitDirect(t, db, id, "ws_test", "crew_test",
				day.Add(time.Duration(i)*time.Minute),
				journal.EntryExecOutputChunk, "output", `{"line":"x"}`)
			total++
		}
	}

	// A high-value entry from the same period must survive compaction.
	emitDirect(t, db, "high_value", "ws_test", "crew_test",
		old.Add(12*time.Hour),
		journal.EntryPeerEscalation, "do not touch me", "{}")

	// A recent low-signal entry must also survive (younger than cutoff).
	emitDirect(t, db, "recent_chunk", "ws_test", "crew_test",
		now.Add(-time.Hour),
		journal.EntryExecOutputChunk, "recent", "{}")

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	ctx := context.Background()
	res, err := c.Run(ctx, "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.BucketsCreated != 3 {
		t.Errorf("buckets: got %d want 3", res.BucketsCreated)
	}
	if res.EntriesDeleted != int64(total) {
		t.Errorf("deleted: got %d want %d", res.EntriesDeleted, total)
	}
	if res.BytesFreed <= 0 {
		t.Errorf("bytes_freed should be positive, got %d", res.BytesFreed)
	}

	// The flush is needed because Run emits the run-marker via the
	// batched writer. Compacted bucket summaries were flushed inside
	// Run before deletes so they are already visible, but the final
	// marker is buffered.
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// No originals remain.
	var remaining int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries
		   WHERE workspace_id = ? AND entry_type = ? AND ts < ?`,
		"ws_test", string(journal.EntryExecOutputChunk),
		now.Add(-30*24*time.Hour).Format(time.RFC3339Nano)).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 0 {
		t.Errorf("expected 0 aged chunks, got %d", remaining)
	}

	// High-value and recent entries survive.
	var survivors int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entries WHERE id IN ('high_value','recent_chunk')`,
	).Scan(&survivors); err != nil {
		t.Fatalf("count survivors: %v", err)
	}
	if survivors != 2 {
		t.Errorf("survivors: got %d want 2", survivors)
	}

	// Bucket summaries (one per day) plus the run marker must now be in
	// the journal under system.compaction.
	entries, _, err := journal.List(ctx, db, journal.Query{
		WorkspaceID: "ws_test",
		Types:       []journal.EntryType{journal.EntrySystemCompaction},
		Limit:       100,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 3 bucket summaries + 1 run marker
	if len(entries) != 4 {
		t.Fatalf("expected 4 system.compaction entries (3 buckets + 1 run marker), got %d", len(entries))
	}

	// Validate the structure of a bucket summary: it must carry
	// compacted_entries in refs with length equal to the bucket size.
	var bucketEntries []journal.Entry
	for _, e := range entries {
		if _, ok := e.Payload["bucket_date"]; ok {
			bucketEntries = append(bucketEntries, e)
		}
	}
	if len(bucketEntries) != 3 {
		t.Fatalf("expected 3 bucket summaries, got %d", len(bucketEntries))
	}
	for _, e := range bucketEntries {
		refs, ok := e.Refs["compacted_entries"].([]any)
		if !ok {
			t.Errorf("bucket %s missing compacted_entries", e.Summary)
			continue
		}
		count, _ := e.Payload["count"].(float64)
		if len(refs) != int(count) {
			t.Errorf("refs count %d != payload count %v", len(refs), count)
		}
	}

	// Old data range must now return only the bucket summaries, never
	// the original chunks, when queried via journal.List.
	old2, _, err := journal.List(ctx, db, journal.Query{
		WorkspaceID: "ws_test",
		Until:       now.Add(-30 * 24 * time.Hour),
		Types:       []journal.EntryType{journal.EntryExecOutputChunk},
		Limit:       500,
	})
	if err != nil {
		t.Fatalf("list old: %v", err)
	}
	if len(old2) != 0 {
		t.Errorf("expected 0 old chunks via List, got %d", len(old2))
	}
}

func TestCompactor_SmallBucketsNotCompacted(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	// 5 entries — below minBucketSize.
	for i := 0; i < 5; i++ {
		emitDirect(t, db, makeID("small", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryContainerMetrics, "m", "{}")
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.BucketsCreated != 0 {
		t.Errorf("expected 0 buckets for under-threshold group, got %d", res.BucketsCreated)
	}
	if res.EntriesDeleted != 0 {
		t.Errorf("expected 0 deletes for under-threshold group, got %d", res.EntriesDeleted)
	}

	// Originals must still be there.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type = ?`,
		string(journal.EntryContainerMetrics)).Scan(&n)
	if n != 5 {
		t.Errorf("expected 5 originals retained, got %d", n)
	}
}

func TestCompactor_OnlyCompactableTypes(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	// 15 peer conversations — NOT compactable.
	for i := 0; i < 15; i++ {
		emitDirect(t, db, makeID("pc", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryPeerConversation, "chat", "{}")
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.BucketsCreated != 0 || res.EntriesDeleted != 0 {
		t.Errorf("peer.conversation must never be compacted, got %+v", res)
	}
}

func TestCompactor_MultipleCompactableTypesBucketSeparately(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()

	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	// 12 chunks + 12 metrics on the SAME DAY must produce two buckets,
	// not one. This catches bucket-key regressions where entry_type gets
	// accidentally dropped from the composite key.
	for i := 0; i < 12; i++ {
		emitDirect(t, db, makeID("c", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryExecOutputChunk, "o", "{}")
		emitDirect(t, db, makeID("m", 0, i), "ws_test", "crew_test",
			old.Add(time.Duration(i)*time.Minute),
			journal.EntryContainerMetrics, "m", "{}")
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	res, err := c.Run(context.Background(), "ws_test", 30*24*time.Hour)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.BucketsCreated != 2 {
		t.Errorf("expected 2 buckets (one per type), got %d", res.BucketsCreated)
	}
	if res.EntriesDeleted != 24 {
		t.Errorf("expected 24 deletes, got %d", res.EntriesDeleted)
	}
}

func TestCompactor_RequiresWorkspace(t *testing.T) {
	c := &Compactor{DB: openDB(t), Journal: &noopEmitter{}, Logger: quietLogger()}
	if _, err := c.Run(context.Background(), "", 0); err == nil {
		t.Error("expected error for empty workspace")
	}
}

func TestNextDailyAt(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		hour int
		want time.Time
	}{
		{
			name: "before target today",
			now:  time.Date(2026, 4, 17, 1, 30, 0, 0, time.UTC),
			hour: 3,
			want: time.Date(2026, 4, 17, 3, 0, 0, 0, time.UTC),
		},
		{
			name: "after target today -> tomorrow",
			now:  time.Date(2026, 4, 17, 5, 0, 0, 0, time.UTC),
			hour: 3,
			want: time.Date(2026, 4, 18, 3, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly at target -> tomorrow",
			now:  time.Date(2026, 4, 17, 3, 0, 0, 0, time.UTC),
			hour: 3,
			want: time.Date(2026, 4, 18, 3, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextDailyAt(tc.now, tc.hour)
			if !got.Equal(tc.want) {
				t.Errorf("got %s want %s", got, tc.want)
			}
		})
	}
}

// makeID produces a stable synthetic journal entry ID for the direct
// inserts. The format matches the "j_<16hex>" shape the real emitter
// uses closely enough that any code path branching on the prefix is
// exercised, without requiring randomness here.
func makeID(tag string, day, i int) string {
	return "j_" + tag + "_" + itoa3(day) + "_" + itoa3(i)
}

func itoa3(n int) string {
	digits := []byte("0123456789")
	return string([]byte{digits[(n/100)%10], digits[(n/10)%10], digits[n%10]})
}
