package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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

// An OWNER must reach the handler on both keeper mutation routes. 404 is a pass
// here — it means authorization let us through and the lookup found no such
// request. 403 is the failure this pins.
func TestKeeperResolveRoute_OwnerIsNotForbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	_ = seedTestWorkspace(t, db, userID) // already enrols the creator as OWNER
	token := mintTokenFor(t, db, userID, "keeper-resolve")

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	for _, tc := range []struct{ name, path, body string }{
		{
			"ask",
			"/api/v1/admin/keeper/ask",
			`{"requesting_agent_id":"a","requesting_crew_id":"c","credential_name":"n","intent":"i"}`,
		},
		{
			"resolve",
			"/api/v1/admin/keeper/requests/kr-nope/resolve",
			`{"decision":"ALLOW"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := routePost(r, token, tc.path, tc.body)
			if rr.Code == http.StatusForbidden {
				t.Fatalf("OWNER was refused on %s: %d %s", tc.path, rr.Code, rr.Body.String())
			}
			if rr.Code == http.StatusNotFound && tc.name == "resolve" {
				return // reached the handler, no such request — the pass we want
			}
		})
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
