package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Exercise the registered routes with real sessions and cryptographically bound
// sidecar tokens. Each tenant has a real encrypted canary and a positive read
// control, so a missing route or an always-denying implementation cannot pass.
func TestTenWorkspaceHTTPIsolation(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	const secret = "ten-workspace-http-session-secret-32"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken(scopeMaster))
	if err != nil {
		t.Fatal(err)
	}
	type tenant struct{ user, ws, crew, agent, cred, token, canary, member string }
	tenants := make([]tenant, 10)
	for i := range tenants {
		p := fmt.Sprintf("isolation-%02d", i)
		x := tenant{user: p + "-user", ws: p + "-ws", crew: p + "-crew", agent: p + "-agent", cred: p + "-cred", canary: p + "-secret-canary"}
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, x.user, x.user+"@example.test")
		s, e := sessions.NewDBStore(db).Create(t.Context(), x.user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		x.token, e = validator.IssueAccessToken(x.user, s.ID, x.user, x.user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
		// Creation must derive ownership from the authenticated session.
		create := httptest.NewRequest("POST", "/api/v1/workspaces", strings.NewReader(fmt.Sprintf(`{"name":%q,"slug":%q}`, p, p)))
		create.Header.Set("Authorization", "Bearer "+x.token)
		create.Header.Set("Content-Type", "application/json")
		created := httptest.NewRecorder()
		router.ServeHTTP(created, create)
		if created.Code != 201 {
			t.Fatalf("create workspace: %d %s", created.Code, created.Body.String())
		}
		var workspace struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &workspace); err != nil {
			t.Fatal(err)
		}
		x.ws = workspace.ID
		var role string
		if err := db.QueryRow(`SELECT id,role FROM workspace_members WHERE workspace_id=? AND user_id=?`, x.ws, x.user).Scan(&x.member, &role); err != nil {
			t.Fatal(err)
		}
		if role != "OWNER" {
			t.Fatalf("creator role = %s", role)
		}
		var members int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=?`, x.ws).Scan(&members); err != nil {
			t.Fatal(err)
		}
		if members != 1 {
			t.Fatalf("new workspace inherited %d memberships", members)
		}
		execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES (?,?,?,?)`, x.crew, x.ws, x.crew, x.crew)
		seedAgentRow(t, db, x.agent, x.ws, x.crew, x.agent, x.agent, "AGENT")
		seedCredentialEnc(t, db, x.ws, x.user, x.cred, x.cred, x.canary)
		execOrFatal(t, db, `UPDATE credentials SET type='API_KEY',provider='anthropic' WHERE id=?`, x.cred)
		tenants[i] = x
	}
	request := func(t *testing.T, token, tokenHeader, ws, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set(tokenHeader, token)
		req.Header.Set("X-Workspace-ID", ws)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, rr.Code, want, rr.Body.String())
		}
		for _, x := range tenants {
			if strings.Contains(rr.Body.String(), x.canary) {
				t.Fatalf("plaintext credential in %s %s", method, path)
			}
		}
		return rr
	}
	for _, a := range tenants {
		t.Run(a.ws, func(t *testing.T) {
			userReq := func(t *testing.T, method, path, body string, want int) *httptest.ResponseRecorder {
				return request(t, "Bearer "+a.token, "Authorization", a.ws, method, path, body, want)
			}
			for _, path := range []string{"/api/v1/credentials", "/api/v1/credentials/" + a.cred, "/api/v1/agents/" + a.agent} {
				rr := userReq(t, "GET", path, "", 200)
				for _, b := range tenants {
					if b.ws != a.ws && (strings.Contains(rr.Body.String(), b.cred) || strings.Contains(rr.Body.String(), b.agent)) {
						t.Fatalf("foreign object in %s", path)
					}
				}
				if path == "/api/v1/credentials" && !strings.Contains(rr.Body.String(), a.cred) {
					t.Fatal("own credential missing")
				}
			}
			crewToken := internaltoken.DeriveCrewToken(scopeMaster, a.ws, a.crew)
			rr := request(t, crewToken, "X-Internal-Token", a.ws, "GET", "/api/v1/internal/credentials", "", 200)
			var creds []struct {
				ID        string `json:"id"`
				Workspace string `json:"workspace_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &creds); err != nil {
				t.Fatal(err)
			}
			if len(creds) != 1 || creds[0].ID != a.cred || creds[0].Workspace != a.ws {
				t.Fatalf("sidecar list: %s", rr.Body.String())
			}
			for _, b := range tenants {
				if a.ws == b.ws {
					continue
				}
				t.Run(b.ws, func(t *testing.T) {
					for _, path := range []string{
						"/api/v1/credentials/" + b.cred,
						"/api/v1/credentials/" + b.cred + "/fields",
						"/api/v1/credentials/" + b.cred + "/audit",
						"/api/v1/agents/" + b.agent,
					} {
						userReq(t, "GET", path, "", 404)
					}
					userReq(t, "GET", "/api/v1/credentials?workspace_id="+b.ws, "", 403)
					request(t, "Bearer "+a.token, "Authorization", b.ws, "GET", "/api/v1/credentials", "", 403)
					userReq(t, "GET", "/api/v1/workspaces/"+b.ws+"/members", "", 403)
					userReq(t, "PATCH", "/api/v1/credentials/"+b.cred, `{"name":"stolen"}`, 404)
					userReq(t, "DELETE", "/api/v1/credentials/"+b.cred, "", 404)
					userReq(t, "PATCH", "/api/v1/workspaces/"+a.ws+"/members/"+b.member, `{"role":"VIEWER"}`, 404)
					request(t, crewToken, "X-Internal-Token", b.ws, "GET", "/api/v1/internal/credentials?workspace_id="+b.ws, "", 403)
					request(t, crewToken, "X-Internal-Token", b.ws, "GET", "/api/v1/internal/agents/"+b.agent+"/resolve", "", 404)
					request(t, crewToken, "X-Internal-Token", b.ws, "PATCH", "/api/v1/internal/credentials/"+b.cred, `{"status":"REVOKED"}`, 404)
				})
			}
		})
	}
	// Denied writes must not silently mutate the foreign records.
	for _, x := range tenants {
		var name, status, role string
		if err := db.QueryRow(`SELECT name,status FROM credentials WHERE id=? AND deleted_at IS NULL`, x.cred).Scan(&name, &status); err != nil {
			t.Fatal(err)
		}
		if name != x.cred || status != "ACTIVE" {
			t.Fatalf("foreign credential changed: %s %s", name, status)
		}
		if err := db.QueryRow(`SELECT role FROM workspace_members WHERE id=?`, x.member).Scan(&role); err != nil {
			t.Fatal(err)
		}
		if role != "OWNER" {
			t.Fatalf("foreign membership changed: %s", role)
		}
	}
}

// Settings must enforce the same authority through direct HTTP calls, including
// a stale browser session after a role change or removal from the workspace.
func TestSettingsAuthorityAndLiveMembershipHTTP(t *testing.T) {
	db := setupTestDB(t)
	ensureEncryptionKey(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	const secret = "settings-authority-http-session-secret"
	const canary = "settings-secret-must-not-leak"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := NewRouter(db, secret, newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	seedCredentialEnc(t, db, workspace, owner, "settings-credential", "SETTINGS_KEY", canary)
	tokens := map[string]string{}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		user := owner
		if role != "OWNER" {
			user = "settings-" + role
			execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, user, user+"@example.test")
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES (?,?,?,?,?)`, user, workspace, user, role, `["chat","credentials:reveal"]`)
		}
		s, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[role], err = validator.IssueAccessToken(user, s.ID, user, user+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	request := func(t *testing.T, role, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens[role])
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s %s: %d want %d: %s", role, method, path, w.Code, want, w.Body.String())
		}
		if !(method == "POST" && strings.HasSuffix(path, "/reveal") && want == 200) && strings.Contains(w.Body.String(), canary) {
			t.Fatal("plaintext in non-reveal response")
		}
		return w
	}
	policy := "/api/v1/credentials/reveal-policy"
	request(t, "OWNER", "PUT", policy, `{"enabled":true}`, 200)
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
		t.Run(role, func(t *testing.T) {
			request(t, role, "GET", "/api/v1/credentials/settings-credential", "", 200)
			if role != "OWNER" {
				request(t, role, "PUT", policy, `{"enabled":false}`, 403)
			}
			want := 200
			if role == "MEMBER" || role == "VIEWER" {
				want = 403
			}
			request(t, role, "GET", policy, "", want)
			rr := request(t, role, "POST", "/api/v1/credentials/settings-credential/reveal", `{"reason":"Rotate the integration credential for scheduled maintenance"}`, want)
			if want == 200 && !strings.Contains(rr.Body.String(), canary) {
				t.Fatal("authorized reveal did not return fixture secret")
			}
			if role == "MEMBER" || role == "VIEWER" {
				request(t, role, "PATCH", "/api/v1/workspaces/"+workspace, `{"allow_privileged_credentials":true}`, 403)
				request(t, role, "PATCH", "/api/v1/workspaces/"+workspace+"/members/settings-"+role, `{"role":"ADMIN"}`, 403)
				request(t, role, "PATCH", "/api/v1/workspaces/"+workspace+"/members/settings-"+role+"/capabilities", `{"grant":["skill.create"]}`, 403)
				request(t, role, "PUT", "/api/v1/workspaces/"+workspace+"/members/settings-"+role+"/access", `{"mode":"trusted","revision":1,"rights":[]}`, 403)
				request(t, role, "DELETE", "/api/v1/credentials/settings-credential", "", 403)
			}
		})
	}
	// Warm capability caches through successful reads, then revoke and restore
	// through the endpoint used by Settings without issuing a new session.
	capsPath := "/api/v1/workspaces/" + workspace + "/members/settings-ADMIN/capabilities"
	request(t, "OWNER", "PATCH", capsPath, `{"revoke":["credentials:reveal"]}`, 200)
	request(t, "ADMIN", "POST", "/api/v1/credentials/settings-credential/reveal", `{"reason":"Rotate the integration credential for scheduled maintenance"}`, 403)
	request(t, "OWNER", "PATCH", capsPath, `{"grant":["credentials:reveal"]}`, 200)
	request(t, "ADMIN", "POST", "/api/v1/credentials/settings-credential/reveal", `{"reason":"Rotate the integration credential for scheduled maintenance"}`, 200)
	// Ownership of a separate personal workspace must not raise this viewer.
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES ('settings-personal','Personal','settings-personal')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('settings-personal-owner','settings-personal','settings-VIEWER','OWNER')`)
	request(t, "VIEWER", "PATCH", "/api/v1/workspaces/settings-personal", `{"name":"Personal renamed"}`, 200)
	request(t, "VIEWER", "PUT", policy, `{"enabled":false}`, 403)
	request(t, "VIEWER", "POST", "/api/v1/credentials/settings-credential/reveal?workspace_id=settings-personal", `{"reason":"Rotate the integration credential for scheduled maintenance"}`, 403)
	request(t, "VIEWER", "GET", "/api/v1/credentials/settings-credential?workspace_id=settings-personal", "", 404)
	// Demote through the real API, then reuse the already-issued ADMIN session.
	request(t, "OWNER", "PATCH", "/api/v1/workspaces/"+workspace+"/members/settings-ADMIN", `{"role":"VIEWER"}`, 200)
	request(t, "ADMIN", "POST", "/api/v1/credentials/settings-credential/reveal", `{"reason":"Rotate the integration credential for scheduled maintenance"}`, 403)
	request(t, "ADMIN", "PATCH", "/api/v1/workspaces/"+workspace, `{"name":"unauthorized"}`, 403)
	request(t, "OWNER", "DELETE", "/api/v1/workspaces/"+workspace+"/members/settings-ADMIN", "", 200)
	request(t, "ADMIN", "GET", "/api/v1/credentials/settings-credential", "", 403)
	request(t, "OWNER", "GET", "/api/v1/credentials/settings-credential", "", 200)
}
