package journal

// Additional coverage for the read-side query API and the FTS5 shadow-table
// trigger contract. Existing tests in this package cover the FTS5 MATCH
// happy paths (queries_fts_test.go) and the Writer batching path
// (emit_writer_test.go); this file fills the remaining gaps the
// dashboard + memory-hybrid-search consumers depend on:
//
//   * direct shadow-table integrity on INSERT/UPDATE/DELETE
//   * keyset pagination across more than two pages
//   * "since yesterday at noon" relative time-range slicing
//   * the full Severity enum surface
//   * empty-workspace-returns-empty-slice (UI iteration guard)
//   * round-trip of the memory.searched event payload that drives
//     PR #385's RecallCount populator
//
// No production code is modified; every test stands up its own in-memory
// SQLite via openTestDB / openTestDBWithFTS and exercises the public
// journal API. Helpers (quietLogger, summaries, openTestDBWithFTS) are
// reused from sibling _test.go files in the same package.

import (
	"context"
	"testing"
	"time"
)

// TestJournal_FTSTriggerSyncsOnInsert_RowAppearsInShadow verifies that
// inserting a journal_entries row via the Writer also populates the
// journal_entries_fts shadow table via the AFTER INSERT trigger. We
// query the shadow table directly (not via List) so a regression that
// breaks the trigger but accidentally still satisfies List's JOIN is
// still caught here.
func TestJournal_FTSTriggerSyncsOnInsert_RowAppearsInShadow(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	_, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "synchronisation alpha keyword",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// MATCH the shadow table directly — no JOIN to journal_entries.
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_fts WHERE journal_entries_fts MATCH ?`,
		`"alpha"`).Scan(&n); err != nil {
		t.Fatalf("shadow match: %v", err)
	}
	if n != 1 {
		t.Errorf("AFTER INSERT trigger: shadow row count = %d, want 1", n)
	}
}

// TestJournal_FTSTriggerSyncsOnUpdate_ShadowReflectsNewSummary verifies the
// AFTER UPDATE trigger deletes the old shadow row and inserts the new one
// so a MATCH on the previous term loses, and a MATCH on the new term
// hits. The UPDATE is issued directly on the base table (the Writer is
// append-only) which is the same surface a future "edit summary"
// migration would touch.
func TestJournal_FTSTriggerSyncsOnUpdate_ShadowReflectsNewSummary(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	id, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "originalbeta term",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE journal_entries SET summary = ? WHERE id = ?`,
		"replacedgamma word", id); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Old term should now be absent from the shadow.
	var nOld int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_fts WHERE journal_entries_fts MATCH ?`,
		`"originalbeta"`).Scan(&nOld); err != nil {
		t.Fatalf("shadow old match: %v", err)
	}
	if nOld != 0 {
		t.Errorf("AFTER UPDATE trigger: old term still in shadow (%d hits)", nOld)
	}

	// New term must hit.
	var nNew int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_fts WHERE journal_entries_fts MATCH ?`,
		`"replacedgamma"`).Scan(&nNew); err != nil {
		t.Fatalf("shadow new match: %v", err)
	}
	if nNew != 1 {
		t.Errorf("AFTER UPDATE trigger: new term not in shadow (%d hits)", nNew)
	}
}

// TestJournal_FTSTriggerSyncsOnDelete_ShadowRowRemoved verifies the
// AFTER DELETE trigger removes the shadow row so MATCH no longer
// returns the deleted entry. journal_entries is append-only in
// production, but the trigger still fires for retention/compaction
// jobs that DELETE expired rows.
func TestJournal_FTSTriggerSyncsOnDelete_ShadowRowRemoved(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	id, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryPeerConversation,
		ActorType:   ActorAgent,
		Summary:     "deletionsigma marker",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Sanity: present before the delete.
	var pre int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_fts WHERE journal_entries_fts MATCH ?`,
		`"deletionsigma"`).Scan(&pre); err != nil {
		t.Fatalf("shadow pre-delete: %v", err)
	}
	if pre != 1 {
		t.Fatalf("setup: shadow row missing before delete (got %d)", pre)
	}

	if _, err := db.ExecContext(ctx,
		`DELETE FROM journal_entries WHERE id = ?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var post int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_fts WHERE journal_entries_fts MATCH ?`,
		`"deletionsigma"`).Scan(&post); err != nil {
		t.Fatalf("shadow post-delete: %v", err)
	}
	if post != 0 {
		t.Errorf("AFTER DELETE trigger: shadow row survived (%d hits)", post)
	}
}

// TestJournal_Pagination25Across3Pages_ReturnsExpectedSizes exercises the
// keyset cursor over a 25-row corpus with pageSize=10. Existing pagination
// coverage only steps through two pages of two; this scenario verifies
// the third page returns the remainder (5 rows) and that the final cursor
// is empty so the UI stops paging. IDs across pages must be disjoint.
func TestJournal_Pagination25Across3Pages_ReturnsExpectedSizes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	// Distinct, descending timestamps so the natural order is stable
	// independent of insertion timing. 25 entries, oldest first → newest
	// at i==24.
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		_, err := w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryPeerConversation,
			ActorType:   ActorAgent,
			Summary:     "row",
			TS:          base.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	seen := make(map[string]struct{}, 25)
	pageSize := 10

	page1, cur1, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: pageSize})
	if err != nil || len(page1) != pageSize {
		t.Fatalf("page 1: err=%v len=%d want=%d", err, len(page1), pageSize)
	}
	if cur1 == "" {
		t.Fatal("page 1: expected non-empty cursor")
	}
	for _, e := range page1 {
		seen[e.ID] = struct{}{}
	}

	page2, cur2, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: pageSize, Cursor: cur1})
	if err != nil || len(page2) != pageSize {
		t.Fatalf("page 2: err=%v len=%d want=%d", err, len(page2), pageSize)
	}
	if cur2 == "" {
		t.Fatal("page 2: expected non-empty cursor")
	}
	for _, e := range page2 {
		if _, dup := seen[e.ID]; dup {
			t.Errorf("page 2 overlap with page 1 on %s", e.ID)
		}
		seen[e.ID] = struct{}{}
	}

	page3, cur3, err := List(ctx, db, Query{WorkspaceID: "ws_test", Limit: pageSize, Cursor: cur2})
	if err != nil {
		t.Fatalf("page 3: err=%v", err)
	}
	if len(page3) != 5 {
		t.Fatalf("page 3 size: got %d want 5", len(page3))
	}
	// Final page is short → no cursor (the implementation only returns
	// a cursor when the page is full).
	if cur3 != "" {
		t.Errorf("page 3 cursor should be empty on short page, got %q", cur3)
	}
	for _, e := range page3 {
		if _, dup := seen[e.ID]; dup {
			t.Errorf("page 3 overlap on %s", e.ID)
		}
		seen[e.ID] = struct{}{}
	}
	if len(seen) != 25 {
		t.Errorf("paged distinct IDs = %d, want 25", len(seen))
	}
}

// TestJournal_SinceYesterdayNoon_ReturnsOnlyLaterSubset emits entries
// scattered across yesterday and today and asks for "since yesterday at
// noon" — the older entries (yesterday morning) must drop out while
// yesterday-afternoon and today entries remain. Pins the relative-
// time-range query path the dashboard "last 24 hours" filter uses.
func TestJournal_SinceYesterdayNoon_ReturnsOnlyLaterSubset(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	now := time.Date(2026, 5, 17, 14, 0, 0, 0, time.UTC)
	yesterdayNoon := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	// Spread: two before yesterday-noon, three after.
	emit := func(ts time.Time, summary string) {
		_, _ = w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryPeerConversation,
			ActorType:   ActorAgent,
			Summary:     summary,
			TS:          ts,
		})
	}
	emit(yesterdayNoon.Add(-6*time.Hour), "before-1")        // yesterday 06:00
	emit(yesterdayNoon.Add(-1*time.Minute), "before-2")      // yesterday 11:59
	emit(yesterdayNoon.Add(30*time.Minute), "after-1")       // yesterday 12:30
	emit(yesterdayNoon.Add(8*time.Hour), "after-2")          // yesterday 20:00
	emit(now.Add(-30*time.Minute), "after-3")                // today 13:30

	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got, _, err := List(ctx, db, Query{WorkspaceID: "ws_test", Since: yesterdayNoon})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("since-yesterday-noon: got %d rows, want 3 (summaries=%v)",
			len(got), summaries(got))
	}
	for _, e := range got {
		if e.Summary == "before-1" || e.Summary == "before-2" {
			t.Errorf("pre-cutoff entry leaked: %s", e.Summary)
		}
	}
}

// TestJournal_SeverityFilter_FullEnumSurface emits one entry per severity
// in the four-value Severity enum (info / notice / warn / error — see
// types.go) and asserts each severity yields its own single hit. Existing
// TestList_FiltersSeverities only exercises two-element IN-clauses; this
// pins the single-element case for every enumerated value so a regression
// that drops one severity from query builder branches surfaces.
func TestJournal_SeverityFilter_FullEnumSurface(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	for _, sev := range []Severity{SeverityInfo, SeverityNotice, SeverityWarn, SeverityError} {
		_, err := w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryRunStarted,
			Severity:    sev,
			ActorType:   ActorAgent,
			Summary:     string(sev),
		})
		if err != nil {
			t.Fatalf("emit %s: %v", sev, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	for _, sev := range []Severity{SeverityInfo, SeverityNotice, SeverityWarn, SeverityError} {
		got, _, err := List(ctx, db, Query{
			WorkspaceID: "ws_test",
			Severities:  []Severity{sev},
		})
		if err != nil {
			t.Fatalf("list %s: %v", sev, err)
		}
		if len(got) != 1 {
			t.Errorf("severity=%s: got %d rows, want 1", sev, len(got))
			continue
		}
		if got[0].Severity != sev {
			t.Errorf("severity=%s: row has severity %q", sev, got[0].Severity)
		}
	}
}

// TestJournal_EmptyWorkspace_ReturnsEmptyNotNil pins the contract the UI
// depends on: an empty result set is `[]Entry{}` (zero-length but
// non-nil) so JSON-encoded responses serialise as `[]`, not `null`.
// Iterating over `null` in the React grid blew up once before — this
// is the regression guard.
func TestJournal_EmptyWorkspace_ReturnsEmptyNotNil(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// A workspace that exists but has zero journal entries.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id) VALUES ('ws_empty')`); err != nil {
		t.Fatalf("create ws_empty: %v", err)
	}

	got, cursor, err := List(context.Background(), db, Query{WorkspaceID: "ws_empty"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got == nil {
		t.Fatal("List returned nil slice; want zero-length non-nil for JSON [] encoding")
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}
	if cursor != "" {
		t.Errorf("empty result should have empty cursor, got %q", cursor)
	}
}

// TestJournal_MemorySearchedEvent_RoundTripsPayload exercises the
// memory.searched event end-to-end via the generic Emit path (no typed
// helper exists today — verified at audit time). The payload shape
// pinned here matches the doc in types.go (query/scope/hit_count/
// hit_chunk_ids) so PR #385's RecallCount populator and the
// observability dashboard rollups can rely on a stable contract.
func TestJournal_MemorySearchedEvent_RoundTripsPayload(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	id, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_searcher",
		Type:        EntryMemorySearched,
		ActorType:   ActorAgent,
		Summary:     "memory search: deploy procedure",
		Payload: map[string]any{
			"query":         "deploy procedure",
			"scope":         "crew_shared",
			"hit_count":     float64(3),
			"hit_chunk_ids": []any{"chunk_a", "chunk_b", "chunk_c"},
		},
	})
	if err != nil {
		t.Fatalf("emit memory.searched: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Filter by the event type so we hit the same path the
	// consolidator's scoring scan uses.
	got, _, err := List(ctx, db, Query{
		WorkspaceID: "ws_test",
		Types:       []EntryType{EntryMemorySearched},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("memory.searched filter: got %d rows, want 1", len(got))
	}
	e := got[0]
	if e.ID != id {
		t.Errorf("id roundtrip: emit=%q list=%q", id, e.ID)
	}
	if e.Type != EntryMemorySearched {
		t.Errorf("type: got %q want %q", e.Type, EntryMemorySearched)
	}
	if e.Payload["query"] != "deploy procedure" {
		t.Errorf("payload.query lost: %v", e.Payload["query"])
	}
	if e.Payload["scope"] != "crew_shared" {
		t.Errorf("payload.scope lost: %v", e.Payload["scope"])
	}
	// JSON numbers round-trip as float64 in untyped maps; the
	// consolidator does the int conversion at the call site.
	if v, ok := e.Payload["hit_count"].(float64); !ok || v != 3 {
		t.Errorf("payload.hit_count: got %v (%T), want 3", e.Payload["hit_count"], e.Payload["hit_count"])
	}
	ids, ok := e.Payload["hit_chunk_ids"].([]any)
	if !ok {
		t.Fatalf("payload.hit_chunk_ids type: got %T, want []any", e.Payload["hit_chunk_ids"])
	}
	if len(ids) != 3 || ids[0] != "chunk_a" || ids[2] != "chunk_c" {
		t.Errorf("payload.hit_chunk_ids contents: %v", ids)
	}
}

