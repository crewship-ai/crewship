package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestMemberAccessRequiresWorkspaceAdminTokenScope(t *testing.T) {
	h := newWsHandlerForTest(t)
	for _, method := range []string{"GET", "PUT"} {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequest(method, "/", bytes.NewBufferString(`{}`))
			ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: "owner"})
			ctx = context.WithValue(ctx, ctxTokenScopes, stringSet{"agents:write": {}})
			w := httptest.NewRecorder()
			if method == "GET" {
				h.GetMemberAccess(w, r.WithContext(ctx))
			} else {
				h.PutMemberAccess(w, r.WithContext(ctx))
			}
			if w.Code != 403 {
				t.Fatalf("scoped token got %d", w.Code)
			}
		})
	}
}

func TestMemberAccessDocumentFencesRejoinedMembership(t *testing.T) {
	h := newWsHandlerForTest(t)
	for _, tc := range []struct {
		id     string
		status int
	}{{"original-member", 200}, {"replacement-member", 404}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("memberId", "original-member")
		w := httptest.NewRecorder()
		h.writeMemberAccessPolicy(w, r, access.Policy{Membership: access.Membership{ID: tc.id, Mode: "restricted", Revision: 1}, Rights: []access.Right{}})
		if w.Code != tc.status {
			t.Fatalf("membership %s: got %d want %d", tc.id, w.Code, tc.status)
		}
		if tc.status == 404 && bytes.Contains(w.Body.Bytes(), []byte(tc.id)) {
			t.Fatal("replacement policy leaked")
		}
	}
}

func TestMemberAccessPolicyAuthenticatedRoutes(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES ('policy-user','policy-user@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('policy-member',?,'policy-user','MEMBER')`, workspace)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES ('policy-crew',?,'Crew','policy-crew')`, workspace)
	seedAgentRow(t, db, "policy-agent", workspace, "policy-crew", "A", "policy-agent", "AGENT")
	const secret = "member-policy-route-secret-32-characters"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, "policy-user"} {
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, "policy@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/workspaces/" + workspace + "/members/policy-member/access"
	request := func(user, method, target, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", user, method, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	read := request(owner, "GET", path, "", 200)
	var p access.Policy
	if err = json.Unmarshal(read.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Mode != "trusted" || p.ID != "policy-member" || p.Rights == nil {
		t.Fatalf("invalid initial policy: %+v", p)
	}
	request("policy-user", "GET", path, "", 404)
	p.Mode = "restricted"
	p.Rights = []access.Right{{Kind: "agent", ID: "policy-agent", Operation: "chat"}}
	body, _ := json.Marshal(p)
	request("policy-user", "PUT", path, string(body), 403)
	request(owner, "PUT", path, string(body), 200)
	request(owner, "PUT", path, string(body), 409)
	// Authenticated restricted members cannot escape the deny-by-default boundary.
	request("policy-user", "GET", path, "", 404)
	read = request(owner, "GET", path, "", 200)
	if err = json.Unmarshal(read.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Rights) != 1 || p.Revision < 2 {
		t.Fatalf("policy not persisted: %+v", p)
	}
	for _, bad := range []string{
		`{"membership_id":"policy-member","revision":2,"mode":"restricted"}`,
		`{"membership_id":"policy-member","revision":2,"mode":"restricted","rights":null}`,
		`{"membership_id":"policy-member","revision":2,"mode":"restricted","rights":[],"actor":"forged"}`,
		string(body) + ` {}`,
	} {
		request(owner, "PUT", path, bad, 400)
	}
	p.Rights = []access.Right{}
	body, _ = json.Marshal(p)
	request(owner, "PUT", path, string(body), 200)
	if err = (access.Store{DB: db}).Check(t.Context(), "policy-user", workspace, access.Right{Kind: "agent", ID: "policy-agent", Operation: "chat"}); err == nil {
		t.Fatal("empty policy did not revoke grant")
	}
	request(owner, "GET", "/api/v1/workspaces/foreign/members/policy-member/access", "", 403)
	// The query can validate our own tenant, but the path must not disagree.
	// The handler uses the path only to reject, never to scope a DB query.
	for _, method := range []string{"GET", "PUT"} {
		request(owner, method, "/api/v1/workspaces/foreign/members/policy-member/access?workspace_id="+workspace, string(body), 404)
	}
}
