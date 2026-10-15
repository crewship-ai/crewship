package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestGuestRoutineViewSelectsDefinitionsWithoutExecutionAuthority(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	for _, user := range []string{"view-client-a", "view-client-b"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user, workspace, user)
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("view-routine-%d", i)
		seedPipelineRow(t, db, workspace, id, id)
		execOrFatal(t, db, `UPDATE pipelines SET definition_json=? WHERE id=?`, fmt.Sprintf(`{"version":1,"steps":[],"private_definition_canary":"definition-%d"}`, i), id)
	}
	store := access.Store{DB: db}
	for _, user := range []string{"view-client-a", "view-client-b"} {
		member, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		var rights []access.Right
		if user == "view-client-a" {
			for i := 0; i < 3; i++ {
				rights = append(rights, access.Right{Kind: "routine", ID: fmt.Sprintf("view-routine-%d", i), Operation: "view"})
			}
			rights = append(rights, access.Right{Kind: "routine", ID: "view-routine-8", Operation: "run"})
		} else {
			rights = []access.Right{{Kind: "routine", ID: "view-routine-9", Operation: "view"}}
		}
		if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewRouter(db, "routine-view-fixture-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, user := range []string{"view-client-a", "view-client-b"} {
		tokens[user] = mintTokenFor(t, db, user, user+strings.Repeat("0", 40))
	}
	call := func(user, method, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(`{"inputs":{}}`))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s %s: %d want %d: %s", user, method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	base := "/api/v1/workspaces/" + workspace + "/restricted-routine-library"
	call("view-client-a", "GET", "/api/v1/workspaces/not-selected/restricted-routine-library?workspace_id="+workspace, 404)
	list := call("view-client-a", "GET", base, 200)
	if strings.Contains(list.Body.String(), "private_definition_canary") || strings.Contains(list.Body.String(), "view-routine-9") {
		t.Fatal("directory leaked definition or other client's routine", list.Body.String())
	}
	var items []guestRoutineLibraryItem
	if err = json.Unmarshal(list.Body.Bytes(), &items); err != nil || len(items) != 4 {
		t.Fatalf("directory %+v %v", items, err)
	}
	for _, item := range items {
		if item.CanRun || item.RuntimeReady {
			t.Fatal("View acquired execution authority", item)
		}
	}
	for i := 0; i < 3; i++ {
		detail := call("view-client-a", "GET", base+fmt.Sprintf("/view-routine-%d", i), 200)
		if !strings.Contains(detail.Body.String(), fmt.Sprintf("definition-%d", i)) {
			t.Fatal("missing shared definition", detail.Body.String())
		}
	}
	call("view-client-a", "GET", base+"/view-routine-8", 404) // Legacy Run remains invocation-only.
	call("view-client-a", "GET", base+"/view-routine-9", 404)
	call("view-client-b", "GET", base+"/view-routine-0", 404)
	call("view-client-b", "GET", base+"/view-routine-9", 200)
	// Runtime absence may return 503; View must never admit an invocation.
	call("view-client-a", "POST", "/api/v1/workspaces/"+workspace+"/pipelines/view-routine-0/run", 503)
	me := call("view-client-a", "GET", "/api/v1/workspaces/"+workspace+"/access/me", 200)
	if !strings.Contains(me.Body.String(), "Explicit View shares") {
		t.Fatal("View explanation depends on agent grants/runtime", me.Body.String())
	}
	directory := call("view-client-a", "GET", "/api/v1/workspaces", 200)
	if !strings.Contains(directory.Body.String(), `"routines"`) {
		t.Fatal("View-only member has no routine navigation", directory.Body.String())
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, owner)
	call("view-client-a", "GET", base+"/view-routine-0", 404)
	list = call("view-client-a", "GET", base, 200)
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatal("inactive issuer shares remain visible", list.Body.String())
	}
	execOrFatal(t, db, `UPDATE users SET suspended_at='' WHERE id='view-client-a'`)
	call("view-client-a", "GET", base, 401)
}
