package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// `source_missing` answers one question on the detail read: is there still a
// row anywhere that can decide this? The detail pane offers a way out of an
// orphaned row on the strength of it, and refuses to offer one on a live
// decision where the PATCH guard would just refuse the click.
//
// It shipped with no server-side test at all, and with `bool,omitempty` — so
// the true arm was on the wire and the false arm was indistinguishable from
// "this endpoint did not compute it", which is what every list row looks like.
// The inbox already holds the opposite rule two files over, for the keeper
// evidence block: "a present fact reporting none", never "a missing field the
// reader can mistake for not checked". A pointer is what makes the false arm
// sayable.
//
// Both arms are pinned here on the raw JSON rather than the struct, because
// the struct cannot tell absent from false either — that is the whole bug.

type rawInboxDetail map[string]json.RawMessage

func getInboxDetailRaw(t *testing.T, h *InboxHandler, userID, wsID, id string) rawInboxDetail {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox/"+id, nil), userID, wsID, "OWNER")
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox get = %d: %s", rr.Code, rr.Body.String())
	}
	var out rawInboxDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return out
}

// seedWaitpointInboxRow writes the inbox row a pipeline waitpoint produces,
// keyed by the token the way pipeline/waitpoints.go keys it. withSource
// decides whether the row it points at exists.
func seedWaitpointInboxRow(t *testing.T, withSource bool) (*InboxHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)

	const token = "wp-token-1"
	if withSource {
		insertWaitpointRow(t, db, token, wsID, "approval", "pending",
			"Approve the production deploy", "2026-08-30T10:00:00Z")
	}
	execOrFatal(t, db, `INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, target_role, title, body_md,
		 sender_type, sender_id, sender_name, state, priority, blocking,
		 payload_json, created_at, updated_at)
		VALUES ('wp-inbox', ?, 'waitpoint', ?, 'ADMIN', 'Approve the production deploy', '',
		 'pipeline', 'release', 'Release pipeline', 'unread', 'high', 1,
		 '{}', datetime('now'), datetime('now'))`, wsID, token)

	return NewInboxHandler(db, newTestLogger(), nil), ownerID, wsID
}

func TestInboxGet_SaysTheSourceIsGone(t *testing.T) {
	h, ownerID, wsID := seedWaitpointInboxRow(t, false)

	got := getInboxDetailRaw(t, h, ownerID, wsID, "wp-inbox")
	raw, ok := got["source_missing"]
	if !ok {
		t.Fatal("no source_missing on a waitpoint whose token row is gone; the " +
			"detail pane has nothing to offer the way out on")
	}
	if string(raw) != "true" {
		t.Errorf("source_missing = %s, want true", raw)
	}
}

// The arm that was unsayable. A live gate must report a checked false, not an
// absent field: absent is what a LIST row carries, where the answer was never
// computed, and a client that cannot tell them apart cannot trust either.
func TestInboxGet_SaysTheSourceIsStillThere(t *testing.T) {
	h, ownerID, wsID := seedWaitpointInboxRow(t, true)

	got := getInboxDetailRaw(t, h, ownerID, wsID, "wp-inbox")
	raw, ok := got["source_missing"]
	if !ok {
		t.Fatal("source_missing absent on a waitpoint whose token row exists — " +
			"indistinguishable from a list row, where nothing was checked")
	}
	if string(raw) != "false" {
		t.Errorf("source_missing = %s, want false", raw)
	}
}

// The list deliberately does not pay a query per row, so the field must stay
// off list rows entirely. If it ever appeared there as `false` it would assert
// something nobody checked.
func TestInboxList_DoesNotClaimToHaveCheckedTheSource(t *testing.T) {
	h, ownerID, wsID := seedWaitpointInboxRow(t, false)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/inbox", nil), ownerID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inbox list = %d: %s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Rows []rawInboxDetail `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	if len(envelope.Rows) == 0 {
		t.Fatalf("no rows listed: %s", rr.Body.String())
	}
	for _, row := range envelope.Rows {
		if _, ok := row["source_missing"]; ok {
			t.Errorf("list row carries source_missing; the list does not run the probe")
		}
	}
}
