package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A grant whose stored panel scope cannot be decoded must be DROPPED, never
// widened.
//
// nil PanelIDs means "every panel" (pages_authz.go covers). The loader used to
// leave the field nil when json.Unmarshal failed and keep the row, so a
// produce grant scoped to three named panels became one on the whole page the
// moment its own column stopped parsing — an access control that opens when
// its storage breaks.
func TestPageGrants_AnUnreadablePanelScopeDropsTheGrantRatherThanWideningIt(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('bob', 'bob@example.com', 'Bob')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-bob', ?, 'bob', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	pagesGrant(t, h, wsID, userID, "fleet-201",
		`{"subject_type":"user","subject":"bob@example.com","level":"produce","panels":["sluzby"]}`)

	// The column stops parsing — a bad migration, a truncated write, a hand
	// edit. Whatever the cause, the answer must not be "more access".
	if _, err := h.db.Exec(`UPDATE page_grants SET panel_ids = '{not json' WHERE subject_id = 'bob'`); err != nil {
		t.Fatalf("corrupt panel_ids: %v", err)
	}

	rec, err := h.loadPage(t.Context(), wsID, "fleet-201")
	if err != nil {
		t.Fatalf("load page: %v", err)
	}
	records, err := h.loadPageGrantRecords(t.Context(), wsID, rec)
	if err != nil {
		t.Fatalf("load grants: %v", err)
	}
	for _, g := range records {
		if g.SubjectID == "bob" {
			t.Fatalf("the grant survived with PanelIDs=%v — nil there means EVERY panel, so an "+
				"unreadable scope just widened it", g.PanelIDs)
		}
	}
}

// A public link cannot be minted with an expiry in the past.
//
// time.Duration is int64 nanoseconds, so days * 24h wraps negative past about
// 106 751 days; the ceiling check then passed and now.Add(negative) produced a
// 201 for a link that was already dead.
func TestPagePublicExpiry_AnOverflowingDayCountIsRefused(t *testing.T) {
	t.Parallel()

	for _, days := range []int{106752, 1 << 40, 1 << 62} {
		rr := httptest.NewRecorder()
		d := days
		if _, ok := pagePublicExpiry(rr, &d, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)); ok {
			t.Errorf("expires_in_days=%d was accepted", days)
		}
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expires_in_days=%d: code %d, want 400", days, rr.Code)
		}
	}
}
