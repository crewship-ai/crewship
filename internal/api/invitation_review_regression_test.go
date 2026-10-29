package api

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/database"
)

func TestInvitationReviewExpiredSameDateCanBeReissued(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	now := time.Now().UTC()
	expired := now.Add(-time.Second)
	if expired.Format("2006-01-02") != now.Format("2006-01-02") {
		t.Fatal("fixture requires same UTC date; rerun after first UTC second")
	}
	seedInvitation(t, db, "review-expired", ws, inviter, "expired-review@example.test", "MEMBER", expired, nil)
	h := NewWorkspaceHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"expired-review@example.test","role":"MEMBER"}`)), inviter, ws, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateInvitation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("expired same-date invitation blocked reissue: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInvitationReviewRevokedInviterCannotGrantAtSignup(t *testing.T) {
	db := setupTestDB(t)
	inviter, ws := seedInviter(t, db)
	seedInvitation(t, db, "review-revoked", ws, inviter, "revoked-review@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
	if _, err := db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE user_id=?`, inviter); err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	rr := httptest.NewRecorder()
	h.Signup(rr, httptest.NewRequest("POST", "/auth/signup", strings.NewReader(`{"email":"revoked-review@example.test","full_name":"Review","password":"Synthetic-Review-2026!","invitation_token":"tok-review-revoked"}`)))
	if rr.Code != 202 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var memberships, accepted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email='revoked-review@example.test'`, ws).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='review-revoked' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 || accepted != 0 {
		t.Fatalf("revoked inviter changed authority: members=%d accepted=%d", memberships, accepted)
	}
}

// Build the genuine pre-invariant schema inside this test's measured time.
// Only schema is shared: each case receives a private in-memory snapshot before
// it seeds legacy authority, runs the real upgrade and exercises Signup.
func TestInvitationReviewInviterSessionCeiling(t *testing.T) {
	template, _ := setupGuestAdministratorUpgradeDB(t)
	newUpgradeDB := func(t *testing.T) (*sql.DB, func()) {
		t.Helper()
		resetCapabilityCache()
		drainBackgroundWork(t)
		snapshot, err := database.SnapshotToMemory(t.Context(), template)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = snapshot.Close() })
		db := snapshot.DB
		// Backup copies database pages, not connection pragmas. Retain the
		// original fixture's foreign-key and single-connection invariants.
		execOrFatal(t, db, "PRAGMA foreign_keys=ON")
		var foreignKeys int
		if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 || db.Stats().MaxOpenConnections != 1 {
			t.Fatalf("private upgrade fixture invariants: foreign_keys=%d max_connections=%d err=%v", foreignKeys, db.Stats().MaxOpenConnections, err)
		}
		var installed int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='guest_administrator_insert'").Scan(&installed); err != nil || installed != 0 {
			t.Fatalf("fixture is not pre-invariant: installed=%d err=%v", installed, err)
		}
		return db, func() {
			t.Helper()
			if err := database.Migrate(t.Context(), db, newTestLogger()); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='guest_administrator_insert'").Scan(&installed); err != nil || installed != 1 {
				t.Fatalf("genuine upgrade did not install invariant: installed=%d err=%v", installed, err)
			}
		}
	}
	for _, mode := range []string{"trusted", "restricted-here", "restricted-other"} {
		t.Run(mode, func(t *testing.T) {
			db, completeUpgrade := newUpgradeDB(t)
			inviter, ws := seedInviter(t, db)
			seedInvitation(t, db, "review-ceiling", ws, inviter, "ceiling-review@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
			if mode == "restricted-here" {
				execOrFatal(t, db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, inviter)
			}
			if mode == "restricted-other" {
				execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('other-ceiling-ws','Other','other-ceiling-ws')`)
				execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role,access_mode) VALUES('other-ceiling-m',?,'other-ceiling-ws','VIEWER','restricted')`, inviter)
			}
			completeUpgrade()
			h := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
			rr := signupWithInvitationProof(t, h, "ceiling-review@example.test", "tok-review-ceiling")
			if rr.Code != 202 {
				t.Fatal(rr.Code, rr.Body.String())
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email='ceiling-review@example.test'`, ws).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "trusted" {
				want = 1
			}
			if n != want {
				t.Fatalf("%s grants=%d want%d", mode, n, want)
			}
		})
	}
}
