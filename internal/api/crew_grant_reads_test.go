package api

import (
	"bytes"
	"os"

	"encoding/json"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v5"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestCrewGrantHTTPReadsAreFilteredAndBounded(t *testing.T) {
	mdb := testutil.MigratedDB(t)
	db := mdb.DB
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('crew-reader','reader@fixture.invalid'),('crew-outsider','outsider@fixture.invalid')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('crew-reader',?,'crew-reader','VIEWER','restricted'),('crew-outsider',?,'crew-outsider','VIEWER','restricted')`, workspace, workspace)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('crew-foreign','Foreign','crew-foreign')`)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug,description,mcp_config_json) VALUES('crew-visible',?,'Visible','visible','SECRET-DESCRIPTION','{"secret":"SECRET-CONFIG"}'),('crew-hidden',?,'HIDDEN-CREW','hidden',NULL,NULL),('crew-foreign','crew-foreign','FOREIGN-CREW','foreign',NULL,NULL)`, workspace, workspace)
	seedAgentRow(t, db, "visible-agent", workspace, "crew-visible", "Visible agent", "visible-agent", "AGENT")
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "crew-reader", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "crew-reader", workspace, "restricted", member, []access.Right{{Kind: "crew", ID: "crew-visible", Operation: "use"}}); err != nil {
		t.Fatal(err)
	}
	outsider, err := store.Membership(t.Context(), "crew-outsider", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "crew-outsider", workspace, "restricted", outsider, []access.Right{{Kind: "agent", ID: "visible-agent", Operation: "discover"}}); err != nil {
		t.Fatal(err)
	}
	// An agent share never grants its parent crew's identity or access explanation.
	counted, counter := testutil.CountingSQLDB(t, mdb.Path(), 1)
	router, err := NewRouter(counted, "crew-read-test-secret-longer-than-32", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, user := range []string{"crew-reader", "crew-outsider"} {
		tokens[user] = mintTokenFor(t, db, user, user+"-session")
	}
	call := func(user, method, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		// Include the registered asynchronous token-use write in each sample.
		if !waitForBackgroundWork(5 * time.Second) {
			t.Fatal("token-use bookkeeping did not finish")
		}
		if rr.Code != want {
			t.Fatalf("%s %s: got%d want%d %s", method, path, rr.Code, want, rr.Body.String())
		}
		for _, secret := range []string{"SECRET-", "HIDDEN-CREW", "FOREIGN-CREW", "mcp_config", "container_", "encrypted_value", "member_count"} {
			if strings.Contains(rr.Body.String(), secret) {
				t.Fatalf("crew projection leaked %s", secret)
			}
		}
		return rr
	}
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("spec.json", bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"CrewList", "CrewRead", "CrewAccessMe"} {
		schemas[name], err = compiler.Compile("spec.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
	}
	checkSchema := func(name string, rr *httptest.ResponseRecorder) {
		t.Helper()
		var body any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(body); err != nil {
			t.Fatalf("actual %s: %v", name, err)
		}
		if object, ok := body.(map[string]any); ok {
			delete(object, map[string]string{"CrewRead": "id", "CrewAccessMe": "crew_id"}[name])
			if err := schemas[name].Validate(object); err == nil {
				t.Fatalf("%s accepted missing identity", name)
			}
		}
	}
	// Warm authentication once; compare complete HTTP query counts at 1 vs31.
	call("crew-reader", "GET", "/api/v1/crews", 200)
	counter.Reset()
	rr := call("crew-reader", "GET", "/api/v1/crews", 200)
	queries := counter.Count()
	checkSchema("CrewList", rr)
	if queries == 0 {
		t.Fatal("SQL counter did not observe HTTP reads")
	}
	t.Logf("complete HTTP crew list: %d SQL queries at 1 and 31 crews", queries)
	if rr.Header().Get("X-Total-Count") != "1" {
		t.Fatal(rr.Header())
	}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("crew-more-%02d", i)
		execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, id, workspace, id, id)
		execOrFatal(t, db, `INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES(?,'crew-reader','crew',?,'view',?,'fixture')`, id, id, owner)
	}
	counter.Reset()
	rr = call("crew-reader", "GET", "/api/v1/crews", 200)
	if got := counter.Count(); got != queries {
		t.Fatalf("HTTP list queries grew: 1crew=%d 31crews=%d", queries, got)
	}
	var list []map[string]any
	if err = json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list) != 31 || rr.Header().Get("X-Total-Count") != "31" {
		t.Fatalf("list/count mismatch: %s %v", rr.Body.String(), err)
	}
	rr = call("crew-reader", "GET", "/api/v1/crews?limit=1&offset=1", 200)
	if err = json.Unmarshal(rr.Body.Bytes(), &list); err != nil || len(list) != 1 || rr.Header().Get("X-Total-Count") != "31" {
		t.Fatal("pagination/count mismatch")
	}
	rr = call("crew-reader", "GET", "/api/v1/crews?q=SECRET-DESCRIPTION", 200)
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatal("secret description affected search")
	}
	rr = call("crew-reader", "GET", "/api/v1/crews/crew-visible", 200)
	checkSchema("CrewRead", rr)
	if !strings.Contains(rr.Body.String(), "visible-agent") {
		t.Fatal("missing safe agent identity")
	}
	rr = call("crew-reader", "GET", "/api/v1/crews/crew-visible/access/me", 200)
	checkSchema("CrewAccessMe", rr)
	var explanation struct {
		Actions map[string]accessMeDecision `json:"actions"`
	}
	if err = json.Unmarshal(rr.Body.Bytes(), &explanation); err != nil || explanation.Actions["view"].State != "allowed" || explanation.Actions["use"].State != "conditional" || explanation.Actions["manage"].State != "denied" {
		t.Fatal("wrong Use explanation", err, rr.Body.String())
	}
	rr = call("crew-reader", "GET", "/api/v1/crews/crew-more-00/access/me", 200)
	if err = json.Unmarshal(rr.Body.Bytes(), &explanation); err != nil || explanation.Actions["use"].State != "denied" {
		t.Fatal("View widened Use", err, rr.Body.String())
	}
	rr = call("crew-reader", "GET", "/api/v1/crews/crew-more-00", 200)
	var empty struct {
		AgentCount int               `json:"agent_count"`
		Agents     []json.RawMessage `json:"agents"`
	}
	if err = json.Unmarshal(rr.Body.Bytes(), &empty); err != nil || empty.AgentCount != 0 || len(empty.Agents) != 0 {
		t.Fatal("visible empty crew was lost", err, rr.Body.String())
	}
	call("crew-reader", "GET", "/api/v1/crews/crew-hidden", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-foreign", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-hidden/access/me", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible/members", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible/container-status", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible/integrations", 404)
	call("crew-reader", "PATCH", "/api/v1/crews/crew-visible", 404)
	call("crew-outsider", "GET", "/api/v1/crews/crew-visible", 404)
	rr = call("crew-outsider", "GET", "/api/v1/crews", 200)
	if strings.TrimSpace(rr.Body.String()) != "[]" || rr.Header().Get("X-Total-Count") != "0" {
		t.Fatal("ungranted list leaked data")
	}
	execOrFatal(t, db, `DELETE FROM access_grants WHERE member_id='crew-reader' AND crew_id='crew-visible'`)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible/access/me", 404)
	execOrFatal(t, db, `INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES('regrant','crew-reader','crew','crew-visible','view',?,'fixture')`, owner)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 200)
	execOrFatal(t, db, `UPDATE crews SET deleted_at=datetime('now') WHERE id='crew-visible'`)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 404)
	execOrFatal(t, db, `UPDATE crews SET deleted_at=NULL WHERE id='crew-visible'`)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 404)
	execOrFatal(t, db, `INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES('issuer-grant','crew-reader','crew','crew-visible','view',?,'fixture')`, owner)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 200)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, workspace)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible", 404)
	call("crew-reader", "GET", "/api/v1/crews/crew-visible/access/me", 404)
}
