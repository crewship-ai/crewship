package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func artifactPolicyCall(t *testing.T, h *PageHandler, ws, user, role, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := artifactPolicyHumanRequest(t, h, method, "/", ws, user, role, body)
	r.SetPathValue("workspaceId", ws)
	w := httptest.NewRecorder()
	if method == "GET" {
		h.GetArtifactCreationPolicy(w, r)
	} else {
		h.UpdateArtifactCreationPolicy(w, r)
	}
	return w
}
func enableArtifactPolicyForTest(t *testing.T, h *PageHandler, ws, user, crew string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"enabled": true, "crew_ids": []string{crew}, "base_version": 0})
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", string(body)), 200)
}
func TestArtifactCreationPolicyDefaultAdminScopeAndCAS(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	if h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("new workspace enabled by default")
	}
	w := artifactPolicyCall(t, h, ws, user, "OWNER", "GET", "")
	artifactPolicyExpect(t, w, 200)
	var initial artifactCreationPolicy
	if json.Unmarshal(w.Body.Bytes(), &initial) != nil || initial.Enabled || initial.Version != 0 || initial.CrewIDs == nil {
		t.Fatalf("bad default %s", w.Body.String())
	}
	pagesSeedUser(t, h, ws, "policy-member", "policy@example.com", "MEMBER")
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, "policy-member", "OWNER", "GET", ""), 403)
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, "policy-member", "OWNER", "PUT", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":0}`), 403)
	enableArtifactPolicyForTest(t, h, ws, user, "crew-lookout")
	if !h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("explicit crew allowlist did not enable")
	}
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":[],"base_version":0}`), 409)
	if !h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("stale update revoked current policy")
	}
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":false,"crew_ids":["crew-lookout"],"base_version":1}`), 200)
	if h.artifactAgentPolicyAllows(t.Context(), ws, "crew-lookout") {
		t.Fatal("workspace disable ineffective")
	}
	// A restricted account cannot retain an administrator role. The caller
	// context stays OWNER so both handlers must recheck the live membership.
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, ws, user)
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "GET", ""), 403)
	artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":2}`), 403)

}
func TestArtifactCreationPolicyRejectsForeignDuplicateAndNullCrews(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	for _, body := range []string{`{}`, `{"enabled":true,"crew_ids":null,"base_version":0}`, `{"enabled":true,"crew_ids":["crew-lookout","crew-lookout"],"base_version":0}`, `{"enabled":true,"crew_ids":["missing-crew"],"base_version":0}`, `{"enabled":null,"crew_ids":[],"base_version":0}`, `{"enabled":true,"crew_ids":[],"base_version":null}`} {
		artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", body), 400)
	}
	var count int
	_ = h.db.QueryRow(`SELECT count(*) FROM workspace_artifact_creation_policy WHERE workspace_id=?`, ws).Scan(&count)
	if count != 0 {
		t.Fatal("invalid policy partially persisted")
	}
}
func TestArtifactPolicyRejectsForgedPathAndQuery(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			r := artifactPolicyHumanRequest(t, h, method, "/?workspace_id=foreign-workspace", ws, user, "OWNER", `{"enabled":true,"crew_ids":["crew-lookout"],"base_version":0}`)
			r.SetPathValue("workspaceId", "foreign-workspace")
			w := httptest.NewRecorder()
			if method == "GET" {
				h.GetArtifactCreationPolicy(w, r)
			} else {
				h.UpdateArtifactCreationPolicy(w, r)
			}
			artifactPolicyExpect(t, w, 404)
		})
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM workspace_artifact_creation_policy`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged scope changed policy: count=%d err=%v", count, err)
	}
}

// Handler fixtures carry a current stored session, matching HTTP admission.
func artifactPolicyHumanRequest(t *testing.T, h *PageHandler, method, target, ws, user, role, body string) *http.Request {
	t.Helper()
	r := pagesRequest(t, method, target, ws, user, role, body)
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "artifact-policy-test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(r.Context(), ctxAuthKind, AuthKindSession)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: user, SessionID: session.ID})
	return r.WithContext(ctx)
}
func artifactPolicyExpect(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status %d want %d: %s", response.Code, status, response.Body.String())
	}
}

// A credential admitted by the real Router must remain current after body
// decoding and the policy writer fence; membership stays unchanged.
func TestArtifactPolicyCredentialChangesDuringBodyRead(t *testing.T) {
	for _, change := range []string{"cli-revoked", "session-revoked", "cli-scope-narrowed", "actor-demoted", "unrelated-cli-revoked", "unrelated-session-revoked"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			body := `{"enabled":true,"crew_ids":[],"base_version":0}`
			request := httptest.NewRequest("PUT", "/api/v1/workspaces/ws-old/artifact-creation-policy", nil)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
			request.Header.Set("Origin", "http://example.com")
			var sessionID, otherSessionID string
			if strings.Contains(change, "session") {
				session, err := f.r.sessionsStore.Create(t.Context(), "wsadmin", "policy", "127.0.0.1", auth.RefreshTokenTTL)
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
				other, err := f.r.sessionsStore.Create(t.Context(), "wsadmin", "other-policy", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				otherSessionID = other.ID
			}
			if change == "unrelated-cli-revoked" {
				mustExec(t, f.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('unrelated-policy-token','wsadmin','other',?,datetime('now'))`, hashStandard("crewship_cli_unrelatedpolicy0000000000"))
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
				case "actor-demoted":
					mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id='ws-old' AND user_id='wsadmin'`)
				case "unrelated-cli-revoked":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-10-16T00:00:00Z' WHERE id='unrelated-policy-token'`)
				case "unrelated-session-revoked":
					mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, otherSessionID)
				}
			}}
			response := httptest.NewRecorder()
			f.r.ServeHTTP(response, request)
			if !read {
				t.Fatalf("credential never reached policy body: %d %s", response.Code, response.Body.String())
			}
			want := 401
			if change == "cli-scope-narrowed" || change == "actor-demoted" {
				want = 403
			}
			unrelated := strings.HasPrefix(change, "unrelated-")
			if unrelated {
				want = 200
			}
			if response.Code != want {
				t.Errorf("%s changed during body read still saved policy: status %d want %d: %s", change, response.Code, want, response.Body.String())
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM workspace_artifact_creation_policy WHERE workspace_id='ws-old'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if unrelated {
				expected = 1
			}
			if count != expected {
				t.Errorf("denied policy persisted: count %d want %d", count, expected)
			}
		})
	}
}

func TestArtifactPolicyListingRequiresAdministrativeScope(t *testing.T) {
	for _, scope := range []string{"pages:read", "workspace:admin", "workspace:*"} {
		t.Run(scope, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			mustExec(t, f.db, `UPDATE cli_tokens SET scopes=? WHERE id='clt-wsadmin'`, `["`+scope+`"]`)
			response := f.do(f.wsAdmin, "GET", "/api/v1/workspaces/ws-old/artifact-creation-policy", "")
			want := 200
			if scope == "pages:read" {
				want = 403
			}
			artifactPolicyExpect(t, response, want)
		})
	}
}

func TestArtifactCreationPolicyBoundsAndStrictJSON(t *testing.T) {
	h, _, _, ws, user := newPagesFixture(t)
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unknown field", `{"enabled":false,"crew_ids":[],"base_version":0,"human_user_id":"other"}`, 400},
		{"second object", `{"enabled":false,"crew_ids":[],"base_version":0} {}`, 400},
		{"wire limit", `{"enabled":false,"crew_ids":["` + strings.Repeat("x", 64<<10) + `"],"base_version":0}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifactPolicyExpect(t, artifactPolicyCall(t, h, ws, user, "OWNER", "PUT", tc.body), tc.status)
		})
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_artifact_creation_policy WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid JSON persisted policy: %d %v", count, err)
	}
}
