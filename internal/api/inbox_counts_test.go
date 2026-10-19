package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type inboxCountsSeed struct {
	id, kind, state, targetRole, payload string
	blocking                             int
}

func seedInboxCountsRow(t *testing.T, h *InboxHandler, wsID string, s inboxCountsSeed, at time.Time) {
	t.Helper()
	seedInboxItem(t, h, wsID, s.id, s.kind, s.state, "", s.targetRole, s.id, at)
	if _, err := h.db.Exec(`UPDATE inbox_items SET blocking = ?, payload_json = ? WHERE id = ?`, s.blocking, s.payload, s.id); err != nil {
		t.Fatalf("shape %s: %v", s.id, err)
	}
}

func getInboxCounts(t *testing.T, h *InboxHandler, userID, wsID, role string) inboxCountsResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/inbox/counts", nil)
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Counts(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("counts status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got inboxCountsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	return got
}

// Every branch of the actionable predicate (isActionableInboxItem in
// inbox-v2-derive.ts), the resolved split and the per-kind breakdown.
func TestInboxHandler_Counts_SpacesAndKinds(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	now := time.Now().UTC()

	seeds := []inboxCountsSeed{
		// action
		{"wp", "waitpoint", "unread", "", `{}`, 0},
		{"esc-proposal", "escalation", "read", "", `{"kind":"skill_proposal"}`, 0},
		{"esc-routine", "escalation", "unread", "", `{"kind":"routine_proposal"}`, 0},
		{"esc-type", "escalation", "unread", "", `{"escalation_type":"credential"}`, 0},
		{"esc-access", "escalation", "unread", "", `{"request_type":"access","request_id":"r1"}`, 0},
		{"human", "run_needs_human", "unread", "", `{}`, 1},
		{"mem-prop", "memory_consolidation", "unread", "", `{"proposal_id":"p1"}`, 0},
		{"sched-missed", "schedule_missed", "unread", "", `{"schedule_id":"s1"}`, 0},
		{"sched-trip", "schedule_circuit_breaker_tripped", "unread", "", `{"schedule_id":"s1"}`, 0},
		// updates: nothing to act on
		{"esc-advisory", "escalation", "unread", "", `{}`, 1},
		{"esc-access-noid", "escalation", "unread", "", `{"request_type":"access"}`, 0},
		{"esc-type-number", "escalation", "unread", "", `{"escalation_type":7}`, 0},
		{"failed", "failed_run", "read", "", `{}`, 0},
		{"mem-noprop", "memory_consolidation", "unread", "", `{}`, 0},
		{"sched-noid", "schedule_missed", "unread", "", `{"schedule_id":""}`, 0},
		{"msg", "message", "unread", "", `{}`, 0},
		// history
		{"done-wp", "waitpoint", "resolved", "", `{}`, 0},
		{"done-failed", "failed_run", "resolved", "", `{}`, 0},
	}
	for _, s := range seeds {
		seedInboxCountsRow(t, h, wsID, s, now)
	}

	got := getInboxCounts(t, h, userID, wsID, "OWNER")
	want := inboxCountsResponse{
		Total: 18,
		// Read is per user: the two rows seeded 'read' have no marker for this
		// caller, so they still count as unread for them (A7).
		Unread: 16,
		Spaces: inboxSpaceCounts{Action: 9, Updates: 7, History: 2},
		ByKind: map[string]inboxSpaceCounts{
			"waitpoint":                        {Action: 1, History: 1},
			"escalation":                       {Action: 4, Updates: 3},
			"run_needs_human":                  {Action: 1},
			"memory_consolidation":             {Action: 1, Updates: 1},
			"schedule_missed":                  {Action: 1, Updates: 1},
			"schedule_circuit_breaker_tripped": {Action: 1},
			"failed_run":                       {Updates: 1, History: 1},
			"message":                          {Updates: 1},
		},
		// entryAttention per active row; no row has a deadline, so all 18 are "none".
		ByAttention: map[string]int{"decision": 3, "input": 1, "review": 4, "repair": 4},
		ByDeadline:  map[string]int{"hour": 0, "today": 0, "soon": 0, "none": 18},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %+v\nwant   %+v", got, want)
	}

	// Sums are consistent and agree with the list endpoint's own totals.
	sum := inboxSpaceCounts{}
	for _, k := range got.ByKind {
		sum.Action += k.Action
		sum.Updates += k.Updates
		sum.History += k.History
	}
	if sum != got.Spaces || got.Total != sum.Action+sum.Updates+sum.History {
		t.Errorf("by_kind %+v does not add up to spaces %+v / total %d", sum, got.Spaces, got.Total)
	}
	req := httptest.NewRequest("GET", "/api/v1/inbox?limit=500", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	var list inboxListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != got.Total || list.UnreadCount != got.Unread {
		t.Errorf("list count/unread = %d/%d, counts total/unread = %d/%d", list.Count, list.UnreadCount, got.Total, got.Unread)
	}
}

// Same visibility rules as the list: role-targeted and user-targeted items
// are counted only for the audience that can see them, and a per-user read
// marker moves an item out of that caller's unread without changing totals.
func TestInboxHandler_Counts_VisibilityByRole(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	now := time.Now().UTC()

	otherUser := "user-counts-other"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other')`, otherUser, otherUser+"@example.com"); err != nil {
		t.Fatal(err)
	}
	// Two workspace-wide items, one per role tier up the ladder, one for the
	// caller, one for someone else, one in another workspace.
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"all-1", "message", "unread", "", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"all-2", "waitpoint", "unread", "", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"viewer-t", "message", "unread", "VIEWER", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"member-t", "message", "unread", "MEMBER", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"manager-t", "waitpoint", "unread", "MANAGER", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"admin-t", "waitpoint", "unread", "ADMIN", `{}`, 0}, now)
	seedInboxCountsRow(t, h, wsID, inboxCountsSeed{"owner-t", "waitpoint", "resolved", "OWNER", `{}`, 0}, now)
	seedInboxItem(t, h, wsID, "mine", "failed_run", "unread", userID, "", "mine", now)
	seedInboxItem(t, h, wsID, "theirs", "failed_run", "unread", otherUser, "", "theirs", now)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-counts-foreign', 'F', 'cf')`); err != nil {
		t.Fatal(err)
	}
	seedInboxItem(t, h, "ws-counts-foreign", "foreign", "message", "unread", "", "", "foreign", now)

	cases := []struct {
		role               string
		total, action, upd int
		history            int
	}{
		// untargeted(2) + mine(1) + role-targeted items at or below the caller.
		{"OWNER", 8, 4, 3, 1},
		{"ADMIN", 7, 4, 3, 0},
		{"MANAGER", 6, 3, 3, 0},
		{"MEMBER", 5, 2, 3, 0},
		{"VIEWER", 4, 2, 2, 0},
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			got := getInboxCounts(t, h, userID, wsID, c.role)
			if got.Total != c.total || got.Spaces.Action != c.action || got.Spaces.Updates != c.upd || got.Spaces.History != c.history {
				t.Fatalf("%s: total=%d spaces=%+v, want total=%d action=%d updates=%d history=%d",
					c.role, got.Total, got.Spaces, c.total, c.action, c.upd, c.history)
			}
		})
	}

	before := getInboxCounts(t, h, userID, wsID, "OWNER")
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES ('all-1', ?)`, userID); err != nil {
		t.Fatal(err)
	}
	after := getInboxCounts(t, h, userID, wsID, "OWNER")
	if after.Unread != before.Unread-1 || after.Total != before.Total {
		t.Errorf("per-user read: unread %d -> %d, total %d -> %d; want unread-1 and the same total",
			before.Unread, after.Unread, before.Total, after.Total)
	}
}

func TestInboxHandler_Counts_EmptyAndUnauthenticated(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	got := getInboxCounts(t, h, userID, wsID, "MEMBER")
	if got.Total != 0 || got.ByKind == nil || len(got.ByKind) != 0 {
		t.Errorf("empty inbox: %+v, want zero totals and an empty (non-null) by_kind", got)
	}
	req := httptest.NewRequest("GET", "/api/v1/inbox/counts", nil)
	rr := httptest.NewRecorder()
	h.Counts(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no auth status = %d, want 401", rr.Code)
	}
}
