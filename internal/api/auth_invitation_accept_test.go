package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthenticatedInvitationAcceptRequiresExactProof(t *testing.T) {
	db := setupTestDB(t)
	inviter, workspace := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('accept-user','accept@example.test'),('wrong-user','wrong@example.test')`)
	seedInvitation(t, db, "accept-proof", workspace, inviter, "accept@example.test", "MEMBER", time.Now().Add(time.Hour), nil)
	router, err := NewRouter(db, "invitation-accept-test-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	token := mintTokenFor(t, db, "accept-user", "accept"+strings.Repeat("0", 40))
	request := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(`{"token":"tok-accept-proof"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("authenticated invitation accept=%d want200: %s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedInvitationProofBoundaries(t *testing.T) {
	for _, scenario := range []string{"wrong-user", "expired", "replay", "rollback", "narrow-token"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupTestDB(t)
			inviter, workspace := seedInviter(t, db)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('accept-user','accept@example.test'),('wrong-user','wrong@example.test')`)
			expiry := time.Now().Add(time.Hour)
			if scenario == "expired" {
				expiry = time.Now().Add(-time.Hour)
			}
			seedInvitation(t, db, "boundary", workspace, inviter, "accept@example.test", "ADMIN", expiry, nil)
			router, err := NewRouter(db, "invitation-boundary-test-secret-long", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			user := "accept-user"
			if scenario == "wrong-user" {
				user = "wrong-user"
			}
			token := mintTokenFor(t, db, user, "boundary"+strings.Repeat("0", 40))
			if scenario == "narrow-token" {
				execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:read"]' WHERE user_id=?`, user)
			}
			if scenario == "rollback" {
				execOrFatal(t, db, `CREATE TRIGGER reject_accept BEFORE UPDATE OF accepted_at ON workspace_invitations BEGIN SELECT RAISE(ABORT,'synthetic atomicity failure');END`)
			}
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", "/api/v1/auth/invitation/accept", strings.NewReader(`{"token":"tok-boundary"}`))
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			rr := request()
			want := 404
			if scenario == "narrow-token" {
				want = 403
			}
			if scenario == "rollback" {
				want = 500
			}
			if scenario == "replay" {
				want = 200
			}
			if rr.Code != want {
				t.Fatalf("status=%d want%d %s", rr.Code, want, rr.Body.String())
			}
			if scenario == "replay" {
				rr = request()
				if rr.Code != 200 {
					t.Fatalf("replay=%d %s", rr.Code, rr.Body.String())
				}
			}
			var members, accepted int
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id IN ('accept-user','wrong-user')`, workspace).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_invitations WHERE id='boundary' AND accepted_at IS NOT NULL`).Scan(&accepted); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if scenario == "replay" {
				expected = 1
			}
			if members != expected || accepted != expected {
				t.Fatalf("atomic authority effect=%d/%d want%d/%d", members, accepted, expected, expected)
			}
		})
	}
}

func TestInvitationRetryNeverRestoresAuthority(t *testing.T) {
	for _, change := range []string{"removed", "suspended", "email-changed", "expired", "demoted"} {
		t.Run(change, func(t *testing.T) {
			db := setupTestDB(t)
			inviter, workspace := seedInviter(t, db)
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('retry-user','retry@example.test')`)
			seedInvitation(t, db, "retry", workspace, inviter, "retry@example.test", "ADMIN", time.Now().Add(time.Hour), nil)
			handler := NewAuthHandler(db, newTestLogger(), newTestJWTValidator(t), nil, true)
			accept := func() *httptest.ResponseRecorder {
				req := requestWithUser("POST", "/api/v1/auth/invitation/accept", "retry-user", "")
				req.Body = io.NopCloser(strings.NewReader(`{"token":"tok-retry"}`))
				rr := httptest.NewRecorder()
				handler.AcceptInvitation(rr, req)
				return rr
			}
			if rr := accept(); rr.Code != 200 {
				t.Fatal(rr.Code, rr.Body.String())
			}
			var consumed string
			if err := db.QueryRow(`SELECT accepted_at FROM workspace_invitations WHERE id='retry'`).Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			want := 404
			switch change {
			case "removed":
				execOrFatal(t, db, `DELETE FROM workspace_members WHERE user_id='retry-user'`)
			case "suspended":
				execOrFatal(t, db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='retry-user'`)
			case "email-changed":
				execOrFatal(t, db, `UPDATE users SET email='changed@example.test' WHERE id='retry-user'`)
			case "expired":
				execOrFatal(t, db, `UPDATE workspace_invitations SET expires_at='2000-01-01T00:00:00Z' WHERE id='retry'`)
				want = 200
			case "demoted":
				execOrFatal(t, db, `UPDATE workspace_members SET role='VIEWER' WHERE user_id='retry-user'`)
				want = 200
			}
			if rr := accept(); rr.Code != want {
				t.Fatal("retry", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='retry-user' AND workspace_id=?`, workspace).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if change == "removed" {
				expected = 0
			}
			if count != expected {
				t.Fatal("retry restored membership", count)
			}
			var after string
			if err := db.QueryRow(`SELECT accepted_at FROM workspace_invitations WHERE id='retry'`).Scan(&after); err != nil || after != consumed {
				t.Fatal("retry changed tokenconsumption", after, err)
			}
			if change == "demoted" {
				var role string
				if err := db.QueryRow(`SELECT role FROM workspace_members WHERE user_id='retry-user'`).Scan(&role); err != nil || role != "VIEWER" {
					t.Fatal("retry restored oldrole", role, err)
				}
			}
		})
	}
}
