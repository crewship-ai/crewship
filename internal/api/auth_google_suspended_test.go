package api

import (
	"net/http"
	"testing"
)

// A suspended account signs in nowhere. The password path refused it; the
// Google callback minted a session anyway, so a suspension of anyone with a
// linked Google account was a suggestion (review follow-up, 2026-09-30).
func TestGoogleCallbackRefusesASuspendedAccount(t *testing.T) {
	db := setupTestDB(t)
	h := newTestGoogleHandler(t, db)
	userID := seedTestUser(t, db)
	mustExec(t, db, `UPDATE users SET suspended_at = '2026-09-30' WHERE id = ?`, userID)
	mustExec(t, db, `INSERT INTO accounts (id, userId, type, provider, providerAccountId) VALUES ('g-susp', ?, 'oauth', 'google', 'g-susp-sub')`, userID)

	rr := covGoogleCallback(t, h, &covGoogleRT{userinfo: `{"sub":"g-susp-sub","email":"test@example.com","name":"Test User"}`}, "state-susp", "/")

	var live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("a suspended account got %d live session(s) through Google", live)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Value != "" && c.MaxAge >= 0 {
			t.Fatalf("a suspended account got a %s cookie", c.Name)
		}
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("status = 200, want a refusal")
	}
}
