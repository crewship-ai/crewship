package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestSignupInvitationRequiresPresentedProof(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	inviter, workspace := seedInviter(t, db)
	seedInvitation(t, db, "proof-required", workspace, inviter, "unproven@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
	response := signupForEnumTest(t, handler, "unproven@example.test")
	if response.Code != http.StatusAccepted {
		t.Fatalf("signup=%d %s", response.Code, response.Body.String())
	}
	var memberships, accepted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email='unproven@example.test'`, workspace).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='proof-required' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if memberships != 0 || accepted != 0 {
		t.Fatalf("invitation without presented proof changed authority: memberships=%d accepted=%d; want0/0", memberships, accepted)
	}
}

func TestSignupPresentedInvitationDoesNotRedeemSibling(t *testing.T) {
	db := setupTestDB(t)
	handler := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), sessions.NewDBStore(db), true)
	inviter, workspace := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('sibling-invite-ws','Sibling','sibling-invite-ws')`)
	seedInvitation(t, db, "chosen", workspace, inviter, "chosen@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	seedInvitation(t, db, "sibling", "sibling-invite-ws", inviter, "chosen@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
	response := signupWithInvitationProof(t, handler, "chosen@example.test", "tok-chosen")
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	var user string
	if err := db.QueryRow(`SELECT id FROM users WHERE email='chosen@example.test'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	var memberships int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id=?`, user).Scan(&memberships); err != nil || memberships != 1 {
		t.Fatal("sibling redemption", memberships, err)
	}
	router, err := NewRouter(db, "signup-invitation-handoff-test-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, user, "signuphandoff"+strings.Repeat("0", 40))
	request := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(`{"token":"tok-chosen"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, request)
	if rr.Code != 200 {
		t.Fatal("signup then authenticated handoff must be no-op success", rr.Code, rr.Body.String())
	}
	var accepted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='sibling' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil || accepted != 0 {
		t.Fatal("sibling token consumed", accepted, err)
	}
}
