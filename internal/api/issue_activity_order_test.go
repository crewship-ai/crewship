package api

// GET .../issues/{identifier}/activity must agree with .../events.
//
// ListActivity ordered by created_at DESC. created_at is a second-granularity
// string and these rows are written in bursts — a status change stamps its
// own row alongside the ones its side effects emit, all inside the same
// second — so the ORDER BY was decorative: ties broke arbitrarily, and an
// issue's `created` row could surface AFTER three status_changed rows written
// later. Two calls could disagree with each other.
//
// mission_activity.seq is the monotonic per-mission cursor, and it is what
// ListEvents already orders by. These tests pin that ListActivity uses it
// too, in the same direction, so a caller comparing the two endpoints does
// not have to reconcile two histories of one table.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func activityReq(userID, wsID, role, crewID, ident string) *http.Request {
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func TestIssue_ListActivity_OrderedBySeqLikeEvents(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-60", "BACKLOG")

	h.logActivity(context.Background(), missionID, "user", userID, "created", "first")
	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "second")
	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "third")
	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "fourth")

	// Give each row a distinct, ASCENDING created_at. Written back to back
	// they all share one second, and a full tie is the case where
	// `ORDER BY created_at DESC` degenerates into SQLite's scan order — which
	// happens to be insertion order, so the old query looked correct. Spread
	// them out and the old ordering shows its true shape: newest first, with
	// `created` pushed to the bottom. That is the reported symptom, and this
	// is what makes this test red before the fix rather than accidentally
	// green.
	spreadActivityTimestamps(t, h, missionID)

	rec := httptest.NewRecorder()
	h.ListActivity(rec, activityReq(userID, wsID, "OWNER", crewID, "ENG-60"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var rows []activityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}
	want := []string{"first", "second", "third", "fourth"}
	for i, r := range rows {
		if r.Details == nil || *r.Details != want[i] {
			t.Errorf("row %d details = %v, want %q — activity is not in seq order", i, r.Details, want[i])
		}
	}
	// The specific symptom: `created` was showing up last.
	if rows[0].Action != "created" {
		t.Errorf("first row action = %q, want created — the issue's creation is the start of its history", rows[0].Action)
	}
}

func TestIssue_ListActivity_KeepsTheMostRecentRowsWhenCapped(t *testing.T) {
	// Ordering oldest-first must not turn the top-50 cap into "the first 50
	// events of the issue's life"; a busy issue would then never show what
	// just happened.
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-61", "BACKLOG")

	const total = 55
	for i := 0; i < total; i++ {
		h.logActivity(context.Background(), missionID, "user", userID, "status_changed", detailFor(i))
	}

	rec := httptest.NewRecorder()
	h.ListActivity(rec, activityReq(userID, wsID, "OWNER", crewID, "ENG-61"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var rows []activityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 50 {
		t.Fatalf("got %d rows, want the 50-row cap", len(rows))
	}
	// The window is the LAST 50 (events 5..54), presented oldest-first.
	if rows[0].Details == nil || *rows[0].Details != detailFor(total-50) {
		t.Errorf("first row = %v, want %q — the cap must drop the OLDEST rows", rows[0].Details, detailFor(total-50))
	}
	if rows[49].Details == nil || *rows[49].Details != detailFor(total-1) {
		t.Errorf("last row = %v, want the newest event %q", rows[49].Details, detailFor(total-1))
	}
}

// spreadActivityTimestamps rewrites created_at so it increases with seq, one
// minute apart — the ordinary case where the two columns agree and the
// direction of the ORDER BY is the only thing that decides the answer.
func spreadActivityTimestamps(t *testing.T, h *IssueHandler, missionID string) {
	t.Helper()
	rows, err := h.db.Query(`SELECT id, seq FROM mission_activity WHERE mission_id = ? ORDER BY seq ASC`, missionID)
	if err != nil {
		t.Fatalf("read activity seqs: %v", err)
	}
	type row struct {
		id  string
		seq int
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, r)
	}
	_ = rows.Close()
	base := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	for i, r := range all {
		if _, err := h.db.Exec(`UPDATE mission_activity SET created_at = ? WHERE id = ?`,
			base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339), r.id); err != nil {
			t.Fatalf("stamp created_at: %v", err)
		}
	}
}

func detailFor(i int) string {
	return "event-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
}
