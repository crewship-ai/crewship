package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type guestPairFixture struct {
	db                      *sql.DB
	router                  *Router
	jwt, session, workspace string
}

func newGuestPairFixture(t *testing.T) guestPairFixture {
	t.Helper()
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('pair-guest','pair-guest@example.test'),('pair-other','pair-other@example.test')`)
	for _, id := range []string{"pair-guest", "pair-other"} {
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, id+"-member", ws, id)
		store := access.Store{DB: db}
		member, err := store.Membership(t.Context(), id, ws)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, id, ws, "restricted", member, nil); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "guest-pair-router-session-test-secret"
	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sessions.NewDBStore(db).Create(t.Context(), "pair-guest", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := v.IssueAccessToken("pair-guest", s.ID, "Guest", "pair-guest@example.test")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-internal-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	return guestPairFixture{db, router, jwt, s.ID, ws}
}
func (f guestPairFixture) call(t *testing.T, credential, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Workspace-ID", f.workspace)
	if credential == f.jwt {
		r.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: credential})
	} else if credential != "" {
		r.Header.Set("Authorization", "Bearer "+credential)
	}
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, r)
	if rr.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, rr.Code, want, rr.Body.String())
	}
	return rr
}
func (f guestPairFixture) start(t *testing.T) string {
	t.Helper()
	rr := f.call(t, f.jwt, "POST", "/api/v1/auth/pair/start", `{}`, 200)
	var out pairStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Code == "" {
		t.Fatal("missing pairing code", err)
	}
	return out.Code
}
func TestGuestPairingRouterSessionAndAuthority(t *testing.T) {
	f := newGuestPairFixture(t)
	code := f.start(t)
	rr := f.call(t, f.jwt, "GET", "/api/v1/auth/pair/poll?code="+code, "", 200)
	var poll pairPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &poll); err != nil || poll.Status != "pending" {
		t.Fatal("own poll not pending", err)
	}
	// A different verified Guest session must not observe the pairing.
	v, _ := auth.NewJWTValidator("guest-pair-router-session-test-secret")
	sid := pairTestSession(t, f.db, "pair-other")
	other, err := v.IssueAccessToken("pair-other", sid, "Other", "pair-other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	hidden := f.call(t, other, "GET", "/api/v1/auth/pair/poll?code="+code, "", 200)
	var hiddenPoll pairPollResponse
	if err := json.Unmarshal(hidden.Body.Bytes(), &hiddenPoll); err != nil || hiddenPoll.Status != "expired" || hiddenPoll.ExpiresAt != "" || hiddenPoll.AdapterHint != "" {
		t.Fatal("foreign pairing disclosed", err)
	}
	redeemed := f.call(t, "", "POST", "/api/v1/auth/pair/redeem", `{"code":"`+code+`"}`, 200)
	var pair pairRedeemResponse
	if err := json.Unmarshal(redeemed.Body.Bytes(), &pair); err != nil || pair.UserID != "pair-guest" || pair.CliToken == "" {
		t.Fatal("bad principal", err)
	}
	created := f.call(t, f.jwt, "POST", "/api/v1/auth/cli-token", `{"name":"comparison"}`, 200)
	var standard struct{ Token string }
	if err := json.Unmarshal(created.Body.Bytes(), &standard); err != nil || standard.Token == "" {
		t.Fatal("missing comparison token", err)
	}
	for _, token := range []string{pair.CliToken, standard.Token} {
		f.call(t, token, "GET", "/api/v1/workspaces/"+f.workspace+"/access/me", "", 200)
		f.call(t, token, "GET", "/api/v1/workspaces/"+f.workspace+"/members", "", 404)
		f.call(t, token, "POST", "/api/v1/auth/cli-token", `{}`, 404)
		f.call(t, token, "POST", "/api/v1/auth/pair/start", `{}`, 403)
		f.call(t, token, "GET", "/api/v1/auth/pair/poll?code="+code, "", 403)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id='pair-guest' AND tier='STANDARD' AND scopes IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("standard token authority count=%d err=%v", count, err)
	}
}
func TestGuestPairingRouterBearerCannotStart(t *testing.T) {
	f := newGuestPairFixture(t)
	token := mintTokenFor(t, f.db, "pair-guest", strings.Repeat("d", 48))
	f.call(t, token, "POST", "/api/v1/auth/pair/start", `{}`, 403)
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_pairings`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pairings=%d err=%v", n, err)
	}
}
func TestGuestPairingRouterSourceLifecycle(t *testing.T) {
	for _, state := range []string{"logout", "revoke", "suspend"} {
		t.Run(state, func(t *testing.T) {
			f := newGuestPairFixture(t)
			code := f.start(t)
			switch state {
			case "logout":
				f.call(t, f.jwt, "POST", "/api/auth/signout", `{}`, 200)
			case "revoke":
				f.call(t, f.jwt, "POST", "/api/v1/auth/sessions/"+f.session+"/revoke", `{}`, 200)
			case "suspend":
				execOrFatal(t, f.db, `UPDATE users SET suspended_at=datetime('now') WHERE id='pair-guest'`)
			}
			f.call(t, "", "POST", "/api/v1/auth/pair/redeem", `{"code":"`+code+`"}`, 400)
			var n int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id='pair-guest'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("minted=%d err=%v", n, err)
			}
		})
	}
}
