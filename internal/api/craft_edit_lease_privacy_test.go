package api

import (
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func craftLeasePrivacyFixture(t *testing.T) (*AdminGDPRHandler, *sql.DB) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	pidExec(t, db, `INSERT INTO users(id,email) VALUES('lease-subject','lease-subject@example.test'),('lease-other','lease-other@example.test')`)
	pidExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('lease-a','A','lease-a'),('lease-b','B','lease-b')`)
	for _, row := range []struct{ page, workspace, actor string }{
		{"lease-raw", "lease-a", "lease-subject"},
		{"lease-prefixed", "lease-a", "user:lease-subject"},
		{"lease-other-holder", "lease-a", "lease-other"},
		{"lease-foreign", "lease-b", "lease-subject"},
	} {
		pidExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Lease fixture','lease-other','{}')`, row.page, row.workspace, row.page)
		pidExec(t, db, `INSERT INTO mooring_edit_leases(page_id,workspace_id,actor_id,session_id,generation,boot_id,expires_ns) VALUES(?,?,?,'private-session',7,'boot-a',99)`, row.page, row.workspace, row.actor)
		for _, actor := range []string{"lease-subject", "user:lease-subject", "lease-other"} {
			pidExec(t, db, `INSERT INTO mooring_edit_lease_requests(page_id,actor_id,session_id,generation) VALUES(?,?,'private-request-session',7)`, row.page, actor)
		}
	}
	return NewAdminGDPRHandler(db, newTestLogger(), t.TempDir()), db
}

func assertCraftLeaseAnonymized(t *testing.T, db *sql.DB, page string) {
	t.Helper()
	var actor, session string
	var generation, expiry int64
	if err := db.QueryRow(`SELECT actor_id,session_id,generation,expires_ns FROM mooring_edit_leases WHERE page_id=?`, page).Scan(&actor, &session, &generation, &expiry); err != nil {
		t.Fatal(err)
	}
	if actor != "" || session != "" || expiry != 0 || generation != 7 {
		t.Fatalf("lease %s retained identity or lost generation: actor=%q session=%q generation=%d expiry=%d", page, actor, session, generation, expiry)
	}
	var requests int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_lease_requests WHERE page_id=?`, page).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("invalidated generation retained requests: %d %v", requests, err)
	}
}

func TestCraftEditLeaseWorkspaceErasureClearsIdentityPreservingGeneration(t *testing.T) {
	h, db := craftLeasePrivacyFixture(t)
	if _, err := h.erasePagesIdentity(t.Context(), "lease-action", "lease-other", "lease-a", "lease-subject"); err != nil {
		t.Fatal(err)
	}
	assertCraftLeaseAnonymized(t, db, "lease-raw")
	assertCraftLeaseAnonymized(t, db, "lease-prefixed")
	var requests int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_lease_requests WHERE page_id='lease-other-holder'`).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("subject requests survived or unrelated request removed: %d %v", requests, err)
	}
	var actor string
	if err := db.QueryRow(`SELECT actor_id FROM mooring_edit_leases WHERE page_id='lease-foreign'`).Scan(&actor); err != nil || actor != "lease-subject" {
		t.Fatalf("foreign workspace was altered: %q %v", actor, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_lease_requests WHERE page_id='lease-foreign'`).Scan(&requests); err != nil || requests != 3 {
		t.Fatalf("foreign requests changed: %d %v", requests, err)
	}
}

func TestCraftEditLeaseHardUserDeleteClearsIdentityPreservingGeneration(t *testing.T) {
	_, db := craftLeasePrivacyFixture(t)
	pidExec(t, db, `DELETE FROM users WHERE id='lease-subject'`)
	for _, page := range []string{"lease-raw", "lease-prefixed", "lease-foreign"} {
		assertCraftLeaseAnonymized(t, db, page)
	}
	var requests int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_lease_requests WHERE actor_id IN ('lease-subject','user:lease-subject')`).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("deleted user still named in requests: %d %v", requests, err)
	}
	var actor string
	if err := db.QueryRow(`SELECT actor_id FROM mooring_edit_leases WHERE page_id='lease-other-holder'`).Scan(&actor); err != nil || actor != "lease-other" {
		t.Fatalf("unrelated holder changed: %q %v", actor, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM mooring_edit_lease_requests WHERE page_id='lease-other-holder'`).Scan(&requests); err != nil || requests != 1 {
		t.Fatalf("unrelated request changed: %d %v", requests, err)
	}
}

func TestCraftEditLeaseErasureRollsBackWithPagesIdentity(t *testing.T) {
	h, db := craftLeasePrivacyFixture(t)
	pidExec(t, db, `INSERT INTO page_versions(page_id,seq,spec_json,author_user_id) VALUES('lease-raw',1,'{}','lease-subject')`)
	pidExec(t, db, `CREATE TRIGGER fail_craft_lease_erasure BEFORE UPDATE OF actor_id ON mooring_edit_leases WHEN OLD.page_id='lease-prefixed' BEGIN SELECT RAISE(ABORT,'review failure'); END`)
	if _, err := h.erasePagesIdentity(t.Context(), "lease-action", "lease-other", "lease-a", "lease-subject"); err == nil {
		t.Fatal("lease erasure failure did not abort whole Pages erasure")
	}
	var author string
	if err := db.QueryRow(`SELECT author_user_id FROM page_versions WHERE page_id='lease-raw'`).Scan(&author); err != nil || author != "lease-subject" {
		t.Fatalf("failed erasure anonymized historical author: %q %v", author, err)
	}
	var actor string
	if err := db.QueryRow(`SELECT actor_id FROM mooring_edit_leases WHERE page_id='lease-raw'`).Scan(&actor); err != nil || actor != "lease-subject" {
		t.Fatalf("failed erasure changed lease: %q %v", actor, err)
	}
}
