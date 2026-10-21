package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestGuestMilestoneManagementIsScopedVersionedAndAtomic(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	store := access.Store{DB: db}
	for i, user := range []string{"milestone-a", "milestone-b"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user, workspace, user)
		project := fmt.Sprintf("milestone-project-%d", i)
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, workspace, project, project)
		member, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "project", ID: project, Operation: "read"}, {Kind: "project", ID: project, Operation: "list"}, {Kind: "project", ID: project, Operation: "manage"}}); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	router, err := NewRouter(db, "guest-milestone-fixture-secret-long-enough", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, user := range []string{"milestone-a", "milestone-b"} {
		tokens[user] = mintTokenFor(t, db, user, user+strings.Repeat("d", 40))
	}
	prefix := "/api/v1/workspaces/" + workspace + "/restricted-projects/"
	base := prefix + "milestone-project-0/milestones"
	call := func(user, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s %s: HTTP %d want %d: %s", user, method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	decode := func(rr *httptest.ResponseRecorder) guestProjectMilestone {
		t.Helper()
		var item guestProjectMilestone
		if err := json.Unmarshal(rr.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	createBody := `{"name":"Release one","description":"Client milestone brief","target_date":"2026-11-01","status":"active"}`
	empty := call("milestone-a", "GET", base, "", 200)
	if empty.Body.String() != "[]\n" || empty.Header().Get("X-Total-Count") != "0" {
		t.Fatal("bad empty collection", empty.Body.String())
	}
	call("milestone-b", "GET", base, "", 404)
	call("milestone-a", "GET", strings.Replace(base, workspace, "not-selected", 1)+"?workspace_id="+workspace, "", 404)
	call("milestone-a", "POST", base, strings.TrimSuffix(createBody, "}")+`,"project_id":"milestone-project-1"}`, 400)
	call("milestone-a", "POST", base, strings.Replace(createBody, "2026-11-01", "2026-02-30", 1), 400)
	first := decode(call("milestone-a", "POST", base, createBody, 201))
	if first.ProjectID != "milestone-project-0" || !first.CanManage || !first.CanDelete || len(first.Version) != 64 {
		t.Fatal("invalid created milestone", first)
	}
	second := decode(call("milestone-a", "POST", base, strings.Replace(createBody, "Release one", "Release two", 1), 201))
	third := decode(call("milestone-a", "POST", base, strings.Replace(createBody, "Release one", "Release three", 1), 201))
	page := call("milestone-a", "GET", base+"?limit=1&offset=1", "", 200)
	var items []guestProjectMilestone
	if err = json.Unmarshal(page.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0].ID != second.ID || page.Header().Get("X-Total-Count") != "3" {
		t.Fatal("pagination or filtered total", items, err, page.Header())
	}
	foreign := decode(call("milestone-b", "POST", prefix+"milestone-project-1/milestones", strings.Replace(createBody, "Client milestone brief", "OTHER_CLIENT_MILESTONE_CANARY", 1), 201))
	call("milestone-a", "GET", base+"/"+foreign.ID, "", 404)
	call("milestone-b", "GET", base+"/"+first.ID, "", 404)
	update := func(item guestProjectMilestone, name string) string {
		return fmt.Sprintf(`{"name":%q,"description":"Reviewed brief","target_date":"","status":"completed","position":%d,"version":%q}`, name, item.Position, item.Version)
	}
	updated := decode(call("milestone-a", "PUT", base+"/"+first.ID, update(first, "Release reviewed"), 200))
	if updated.Name != "Release reviewed" || updated.Version == first.Version || updated.TargetDate != "" {
		t.Fatal("update/version missing", updated)
	}
	call("milestone-a", "PUT", base+"/"+first.ID, update(first, "Stale draft"), 409)
	// A trusted edit outside this API also invalidates the reviewed snapshot.
	execOrFatal(t, db, `UPDATE milestones SET description='Staff milestone edit' WHERE id=?`, first.ID)
	call("milestone-a", "PUT", base+"/"+first.ID, update(updated, "Stale after staff edit"), 409)
	reviewed := decode(call("milestone-a", "GET", base+"/"+first.ID, "", 200))
	execOrFatal(t, db, `CREATE TRIGGER reject_guest_milestone_audit BEFORE INSERT ON journal_entries WHEN NEW.summary LIKE 'Guest % a project milestone' BEGIN SELECT RAISE(ABORT,'synthetic milestone audit failure'); END`)
	call("milestone-a", "POST", base, createBody, 500)
	call("milestone-a", "PUT", base+"/"+first.ID, update(reviewed, "Must roll back"), 500)
	call("milestone-a", "DELETE", base+"/"+third.ID, fmt.Sprintf(`{"version":%q}`, third.Version), 500)
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM milestones WHERE project_id='milestone-project-0'`).Scan(&count); err != nil || count != 3 {
		t.Fatal("audit failure mutated rows", count, err)
	}
	if decode(call("milestone-a", "GET", base+"/"+first.ID, "", 200)).Name != "Release reviewed" {
		t.Fatal("audit failure committed edit")
	}
	execOrFatal(t, db, `DROP TRIGGER reject_guest_milestone_audit`)
	// Deletion never unlinks issue work; even a stale cross-project reference
	// remains untouched and returns a conflict instead.
	crew := seedTestCrew(t, db, workspace)
	seedAgentRow(t, db, "milestone-lead", workspace, crew, "Lead", "milestone-lead", "LEAD")
	issue := seedIssue(t, db, workspace, crew, "milestone-lead", "MILE-1", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET project_id='milestone-project-1',milestone_id=? WHERE id=?`, first.ID, issue)
	linked := decode(call("milestone-a", "GET", base+"/"+first.ID, "", 200))
	if linked.CanDelete {
		t.Fatal("linked milestone advertised removal")
	}
	call("milestone-a", "DELETE", base+"/"+first.ID, fmt.Sprintf(`{"version":%q}`, linked.Version), 409)
	var retained string
	if err = db.QueryRow(`SELECT milestone_id FROM missions WHERE id=?`, issue).Scan(&retained); err != nil || retained != first.ID {
		t.Fatal("foreign issue link mutated", retained, err)
	}
	call("milestone-a", "DELETE", base+"/"+third.ID, fmt.Sprintf(`{"version":%q}`, strings.Repeat("f", 64)), 409)
	call("milestone-a", "DELETE", base+"/"+third.ID, fmt.Sprintf(`{"version":%q}`, third.Version), 204)
	call("milestone-a", "GET", base+"/"+third.ID, "", 404)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE user_id='milestone-a'`)
	narrow := decode(call("milestone-a", "GET", base+"/"+second.ID, "", 200))
	if narrow.CanManage || narrow.CanDelete {
		t.Fatal("narrow credential advertised writes", narrow)
	}
	call("milestone-a", "POST", base, createBody, 403)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes=NULL WHERE user_id='milestone-a'`)
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, workspace)
	call("milestone-a", "GET", base, "", 404)
	call("milestone-a", "POST", base, createBody, 404)
}
