package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
)

// Membership remains unchanged while middleware admits an exact credential and
// the request-body read revokes it. Custody must revalidate after its fence.
func TestCustodyCredentialChangesDuringBodyRead(t *testing.T) {
	for _, change := range []string{"cli-revoked", "session-revoked", "cli-scope-narrowed", "unrelated-cli-revoked", "unrelated-session-revoked"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			seedInstanceUser(t, f.db, "departing", "departing@example.invalid", "ws-old", "MEMBER")
			seedInstanceUser(t, f.db, "reader", "reader@example.invalid", "ws-old", "MEMBER")
			wmSeedPage(t, f.db, "ws-old", "private-output", "private-output", "Private output", "departing", "")
			mustExec(t, f.db, `CREATE TABLE documents(page_id TEXT PRIMARY KEY REFERENCES pages(id))`)
			mustExec(t, f.db, `INSERT INTO documents VALUES('private-output')`)
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('private-output','user','reader','read','departing')`)
			body := `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`
			request := httptest.NewRequest("POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", nil)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
			request.Header.Set("Origin", "http://example.com")
			var sessionID, otherSessionID string
			if strings.Contains(change, "session") {
				session, err := f.r.sessionsStore.Create(t.Context(), "wsadmin", "custody", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = session.ID
				token, err := f.r.authMw.validator.IssueAccessToken("wsadmin", session.ID, "Workspace admin", "wsadmin@ex.com")
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Del("Authorization")
				request.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: token})
				other, err := f.r.sessionsStore.Create(t.Context(), "wsadmin", "other-custody", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				otherSessionID = other.ID
			}
			if change == "unrelated-cli-revoked" {
				mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('unrelated-custody-token','wsadmin','other',?,datetime('now'))`, hashStandard("crewship_cli_unrelatedcustody0000000000"))
			}
			read := false
			request.Body = &ownershipChangingBody{Reader: strings.NewReader(body), change: func() {
				read = true
				switch change {
				case "cli-revoked":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-10-16T00:00:00Z' WHERE id='clt-wsadmin'`)
				case "session-revoked":
					mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, sessionID)
				case "cli-scope-narrowed":
					mustExec(t, f.db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE id='clt-wsadmin'`)
				case "unrelated-cli-revoked":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-10-16T00:00:00Z' WHERE id='unrelated-custody-token'`)
				case "unrelated-session-revoked":
					mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, otherSessionID)
				}
			}}
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, request)
			if !read {
				t.Fatalf("credential never passed middleware admission: %d %s", response.Code, response.Body.String())
			}
			want := 401
			if change == "cli-scope-narrowed" {
				want = 403
			}
			unrelated := strings.HasPrefix(change, "unrelated-")
			if unrelated {
				want = 200
			}
			if response.Code != want {
				t.Errorf("%s credential changed during body read still transferred custody: status %d want %d: %s", change, response.Code, want, response.Body.String())
			}
			var owner, role string
			if err := f.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&owner); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id='ws-old' AND user_id='wsadmin'`).Scan(&role); err != nil || role != "ADMIN" {
				t.Fatalf("membership changed instead of credential: %s %v", role, err)
			}
			var notices int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE source_id='private-output'`).Scan(&notices); err != nil {
				t.Fatal(err)
			}
			if unrelated {
				if owner != "reader" || notices != 1 {
					t.Fatalf("revoking an unrelated credential affected the current one: owner %s notices %d", owner, notices)
				}
			} else if owner != "departing" || notices != 0 {
				t.Errorf("revoked credential changed custody or inbox: owner %s notices %d", owner, notices)
			}
		})
	}
}

func TestCustodyCredentialListingRequiresAdministrativeScope(t *testing.T) {
	for _, scope := range []string{"agents:write", "workspace:admin", "workspace:*"} {
		t.Run(scope, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			mustExec(t, f.db, `UPDATE cli_tokens SET scopes=? WHERE id='clt-wsadmin'`, `["`+scope+`"]`)
			response := f.do(f.wsAdmin, "GET", "/api/v1/workspaces/ws-old/page-ownership", "")
			want := 200
			if scope == "agents:write" {
				want = 403
			}
			if response.Code != want {
				t.Fatalf("administrative custody listing accepted narrow CLI scope: %s status %d want %d: %s", scope, response.Code, want, response.Body.String())
			}
		})
	}
}
