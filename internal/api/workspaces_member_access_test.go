package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
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

	explained := request(owner, "GET", path+"/effective", "", 200)
	var effective memberEffectiveAccess
	if err = json.Unmarshal(explained.Body.Bytes(), &effective); err != nil || effective.ID != p.ID || !effective.AccountRestricted || len(effective.Decisions) != 1 || effective.Decisions[0].Operation != "chat" {
		t.Fatalf("effective=%+v %v", effective, err)
	}
	request("policy-user", "GET", path+"/effective", "", 404)
	ownPath := "/api/v1/workspaces/" + workspace + "/access/me"
	own := request("policy-user", "GET", ownPath+"?user_id="+owner+"&member_id=forged", "", 200)
	var ownEffective memberEffectiveAccess
	if err = json.Unmarshal(own.Body.Bytes(), &ownEffective); err != nil || ownEffective.ID != "policy-member" || len(ownEffective.Decisions) != 1 || ownEffective.Decisions[0].ID != "policy-agent" || ownEffective.Decisions[0].Name != "A" {
		t.Fatalf("own access=%+v err=%v", ownEffective, err)
	}
	if own.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("own policy must not be cached by intermediaries")
	}
	ownerAccess := request(owner, "GET", ownPath, "", 200)
	if err = json.Unmarshal(ownerAccess.Body.Bytes(), &ownEffective); err != nil || ownEffective.ID == "policy-member" || len(ownEffective.Decisions) != 0 {
		t.Fatalf("owner received guest policy: %+v %v", ownEffective, err)
	}
	request("policy-user", "GET", "/api/v1/workspaces/foreign/access/me?workspace_id="+workspace, "", 404)
	for _, bad := range []string{
		`{"membership_id":"policy-member","revision":2,"mode":"restricted"}`,
		`{"membership_id":"policy-member","revision":2,"mode":"restricted","rights":null}`,
		`{"membership_id":"policy-member","revision":2,"mode":"restricted","rights":[],"actor":"forged"}`,
		string(body) + ` {}`,
	} {
		request(owner, "PUT", path, bad, 400)
	}

	// A routine grant alone does not grant its agent dependency. The own endpoint
	// must explain this even though the saved routine right itself is valid.
	definition := `{"dsl_version":"1.0","name":"Client routine","steps":[{"id":"first","type":"agent_run","agent_slug":"policy-agent","prompt":"Hello"}]}`
	sum := sha256.Sum256([]byte(definition))
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('policy-routine',?,'client-routine','Client routine',?,?,'policy-crew','active')`, workspace, definition, hex.EncodeToString(sum[:]))
	p.Rights = []access.Right{{Kind: "routine", ID: "policy-routine", Operation: "run"}}
	body, _ = json.Marshal(p)
	saved := request(owner, "PUT", path, string(body), 200)
	if err = json.Unmarshal(saved.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	own = request("policy-user", "GET", ownPath, "", 200)
	if err = json.Unmarshal(own.Body.Bytes(), &ownEffective); err != nil {
		t.Fatal(err)
	}
	if len(ownEffective.Decisions) != 1 || ownEffective.Decisions[0].State != "denied" || !strings.Contains(ownEffective.Decisions[0].Reason, "agent run/delegate grants") {
		t.Fatalf("routine without agent grant not explained: %s", own.Body.String())
	}
	p.Rights = []access.Right{}
	body, _ = json.Marshal(p)
	request(owner, "PUT", path, string(body), 200)
	own = request("policy-user", "GET", ownPath, "", 200)
	if err = json.Unmarshal(own.Body.Bytes(), &ownEffective); err != nil || len(ownEffective.Decisions) != 0 {
		t.Fatalf("revoked grants still explained: %+v %v", ownEffective, err)
	}
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

// Deterministically reproduce deletion after authorization but before the
// separate name read. Soft deletion does not change the membership revision.
func TestEffectiveAccessNameDeletionAfterCheck(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('name-crew',?,'Crew','name-crew')`, ws)
	seedAgentRow(t, db, "name-agent", ws, "name-crew", "Before deletion", "name-agent", "AGENT")
	right := access.Right{Kind: "agent", ID: "name-agent", Operation: "chat"}
	store := access.Store{DB: db}
	if err := store.Check(t.Context(), owner, ws, right); err != nil {
		t.Fatal(err)
	}
	before, err := store.Membership(t.Context(), owner, ws)
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE agents SET deleted_at='deleted' WHERE id='name-agent'`)
	name, err := effectiveAccessResourceName(t.Context(), db, ws, right)
	if !errors.Is(err, access.ErrDenied) || name != "" {
		t.Fatalf("name deletion should become denied row: %q %v", name, err)
	}
	after, err := store.Membership(t.Context(), owner, ws)
	if err != nil || before != after {
		t.Fatalf("test no longer reproduces race without epoch change: %+v %+v %v", before, after, err)
	}
}
