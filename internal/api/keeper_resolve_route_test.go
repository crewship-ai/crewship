package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The resolve tests call HandleResolve directly and build the context by hand.
// That proves the handler's own rules and nothing about the route: on dev2 an
// OWNER got a 403 from `crewship keeper resolve` while `crewship keeper ask` —
// registered on the same line, with the same roleManage — went through.
//
// A handler test cannot see that, because it IS the thing that sets the context
// the handler reads. So this drives the real Router, with a real token, the way
// the CLI does.
func routePost(r *Router, token, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// An OWNER must reach the handler on the resolve route AND get its own answer.
//
// The first version of this accepted anything except 403, which meant an
// unregistered route — 404 — passed it. That is precisely the wiring failure the
// test was added to detect, so it could not have detected it. It now seeds a
// real escalated request and demands the 200 that only the handler can produce.
func TestKeeperResolveRoute_OwnerCanResolveThroughTheRouter(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	// seedKeeperFixture's user is the workspace OWNER; mint them a token.
	var userID string
	if err := db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id = ? AND role = 'OWNER'`,
		wsID).Scan(&userID); err != nil {
		t.Fatalf("find the owner: %v", err)
	}
	token := mintTokenFor(t, db, userID, "keeper-resolve")

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-route', 'access', ?, ?, ?, 'rotate the certs', 'ESCALATE')`,
		agentID, crewID, credID)

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rr := routePost(r, token,
		"/api/v1/admin/keeper/requests/kr-route/resolve?workspace_id="+wsID,
		`{"decision":"DENY","reason":"no change window"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("OWNER could not resolve through the router: %d %s", rr.Code, rr.Body.String())
	}

	// And it actually decided, rather than answering 200 from somewhere else.
	var decision string
	if err := db.QueryRow(`SELECT COALESCE(decision,'') FROM keeper_requests WHERE id = 'kr-route'`).
		Scan(&decision); err != nil {
		t.Fatalf("read decision: %v", err)
	}
	if decision != "DENY" {
		t.Errorf("decision is %q, want DENY — a 200 that changed nothing is not the route working", decision)
	}
}

// The ask route is registered on the same line with the same role, and it was
// the control that told me the resolve 403 was NOT a wiring problem. An
// unregistered route answers 404/405; reaching the handler with a body naming
// nothing real gets that handler's own rejection instead.
func TestKeeperAskRoute_OwnerReachesTheHandler(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID) // already enrols the creator as OWNER
	token := mintTokenFor(t, db, userID, "keeper-ask")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rr := routePost(r, token, "/api/v1/admin/keeper/ask?workspace_id="+wsID,
		`{"requesting_agent_id":"nope","requesting_crew_id":"nope","credential_name":"ghost-key","intent":"i"}`)
	if rr.Code == http.StatusForbidden {
		t.Fatalf("OWNER was refused on the ask route: %s", rr.Body.String())
	}
	// The status alone cannot settle this: the handler's own "no such credential"
	// is a 404, and so is an unregistered route. The BODY can — only the handler
	// knows which credential was asked for.
	if !strings.Contains(rr.Body.String(), "ghost-key") {
		t.Fatalf("the ask route did not reach its handler: %d %s", rr.Code, rr.Body.String())
	}
}

// And the refusal still has to bite: a MEMBER must not be able to rule on a
// credential escalation, which is the property the whole audience change rests
// on.
func TestKeeperResolveRoute_MemberIsForbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	token := seedRoleMemberToken(t, db, wsID, "member-user", "MEMBER", "keeper-member")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	rr := routePost(r, token,
		"/api/v1/admin/keeper/requests/kr-nope/resolve?workspace_id="+wsID,
		`{"decision":"ALLOW"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a MEMBER reached the resolve route: %d %s", rr.Code, rr.Body.String())
	}
}
