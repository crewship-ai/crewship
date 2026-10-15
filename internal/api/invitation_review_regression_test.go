package api

import (
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestInvitationReviewInviterSessionCeiling(t *testing.T) {
	for _, mode := range []string{"trusted", "restricted-here", "restricted-other"} {
		t.Run(mode, func(t *testing.T) {
			db, completeUpgrade := setupGuestAdministratorUpgradeDB(t)
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
