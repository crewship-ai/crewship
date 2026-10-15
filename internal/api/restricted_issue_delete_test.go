package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestGuestIssueDeletionIsVersionedScopedAndAtomic(t *testing.T) {
	f := setupDeletionFixture(t)
	db := f.h.db
	store := access.Store{DB: db}
	for _, project := range []string{"delete-project-a", "delete-project-b"} {
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, f.workspace, project, project)
	}
	execOrFatal(t, db, `UPDATE missions SET project_id='delete-project-a' WHERE id=?`, f.issue)
	tokens := map[string]string{}
	for _, user := range []string{"delete-guest-a", "delete-guest-b"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,user_id,workspace_id,role) VALUES(?,?,?,'VIEWER')`, user, user, f.workspace)
		member, err := store.Membership(t.Context(), user, f.workspace)
		if err != nil {
			t.Fatal(err)
		}
		project := "delete-project-a"
		if user == "delete-guest-b" {
			project = "delete-project-b"
		}
		if _, err = store.Replace(t.Context(), f.user, user, f.workspace, "restricted", member, []access.Right{{Kind: "project", ID: project, Operation: "read"}, {Kind: "project", ID: project, Operation: "manage"}, {Kind: "project", ID: project, Operation: "delete_issues"}}); err != nil {
			t.Fatal(err)
		}
		tokens[user] = mintTokenFor(t, db, user, user+strings.Repeat("x", 40))
	}
	router, err := NewRouter(db, "guest-delete-synthetic-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	base := "/api/v1/workspaces/" + f.workspace + "/restricted-issues/" + f.issue
	call := func(user, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", f.workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s %s: %d want%d %s", user, method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	preview := func() guestIssueDeletion {
		t.Helper()
		rr := call("delete-guest-a", "GET", base+"/deletion", "", 200)
		var d guestIssueDeletion
		if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	body := func(d guestIssueDeletion) string {
		return fmt.Sprintf(`{"project_id":%q,"version":%q,"confirm":true}`, d.ProjectID, d.Version)
	}
	initial := preview()
	if !initial.CanDelete || initial.ProjectID != "delete-project-a" {
		t.Fatal("missing delete projection", initial)
	}
	call("delete-guest-b", "GET", base+"/deletion", "", 404)
	call("delete-guest-b", "DELETE", base, body(initial), 404)
	call("delete-guest-a", "DELETE", strings.Replace(base, f.workspace, "foreign-workspace", 1)+"?workspace_id="+f.workspace, body(initial), 404)
	call("delete-guest-a", "DELETE", base, strings.Replace(body(initial), `"confirm":true`, `"confirm":false`, 1), 400)
	execOrFatal(t, db, `UPDATE missions SET aggregate_revision=aggregate_revision+1 WHERE id=?`, f.issue)
	call("delete-guest-a", "DELETE", base, body(initial), 409)
	member, err := store.Membership(t.Context(), "delete-guest-a", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "project", ID: "delete-project-a", Operation: "read"}, {Kind: "project", ID: "delete-project-a", Operation: "manage"}}
	member, err = store.Replace(t.Context(), f.user, "delete-guest-a", f.workspace, "restricted", member, rights)
	if err != nil {
		t.Fatal(err)
	}
	call("delete-guest-a", "GET", base+"/deletion", "", 404)
	call("delete-guest-a", "DELETE", base, body(initial), 404)
	if _, err = store.Replace(t.Context(), f.user, "delete-guest-a", f.workspace, "restricted", member, append(rights, access.Right{Kind: "project", ID: "delete-project-a", Operation: "delete_issues"})); err != nil {
		t.Fatal(err)
	}
	// Active execution and assignment guards are independent of visible status.
	var lead string
	if err = db.QueryRow(`SELECT id FROM agents WHERE crew_id=? AND agent_role='LEAD' LIMIT 1`, f.crew).Scan(&lead); err != nil {
		t.Fatal(err)
	}
	child := seedIssue(t, db, f.workspace, f.crew, lead, "FOREIGN-CHILD", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET project_id='delete-project-b',parent_issue_id=? WHERE id=?`, f.issue, child)
	parent := preview()
	if parent.CanDelete {
		t.Fatal("parent issue offered deletion")
	}
	call("delete-guest-a", "DELETE", base, body(parent), 409)
	var parentID string
	if err = db.QueryRow(`SELECT parent_issue_id FROM missions WHERE id=?`, child).Scan(&parentID); err != nil || parentID != f.issue {
		t.Fatal("foreign child relation changed", parentID, err)
	}
	execOrFatal(t, db, `DELETE FROM missions WHERE id=?`, child)
	execOrFatal(t, db, `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at) VALUES('guest-delete-execution',?,0,0,'working',?,'2026-10-15T00:00:00Z','2026-10-15T00:00:00Z')`, f.issue, lead)
	active := preview()
	if active.CanDelete {
		t.Fatal("active execution offered deletion")
	}
	call("delete-guest-a", "DELETE", base, body(active), 409)
	execOrFatal(t, db, `UPDATE issue_executions SET stage='accepted' WHERE id='guest-delete-execution'`)
	seedMissionAssignment(t, f.h, f.workspace, f.issue, lead, "guest-delete-assignment", "PENDING", "", "", "")
	execOrFatal(t, db, `UPDATE assignments SET issue_execution_id='guest-delete-execution' WHERE id='guest-delete-assignment'`)
	active = preview()
	if active.CanDelete {
		t.Fatal("active assignment offered deletion")
	}
	call("delete-guest-a", "DELETE", base, body(active), 409)
	execOrFatal(t, db, `UPDATE assignments SET status='COMPLETED' WHERE id='guest-delete-assignment'`)
	execOrFatal(t, db, `UPDATE mission_tasks SET status='COMPLETED' WHERE assignment_id='guest-delete-assignment'`)
	current := preview()
	// A failed receipt append must restore the issue and the old chat anchors.
	execOrFatal(t, db, `CREATE TRIGGER reject_guest_delete_ref BEFORE INSERT ON chat_entity_refs WHEN EXISTS(SELECT 1 FROM agent_operations WHERE id=NEW.operation_id AND verb='deleted') BEGIN SELECT RAISE(ABORT,'synthetic receipt failure');END`)
	call("delete-guest-a", "DELETE", base, body(current), 500)
	if !preview().CanDelete {
		t.Fatal("receipt failure changed work")
	}
	execOrFatal(t, db, `DROP TRIGGER reject_guest_delete_ref`)
	execOrFatal(t, db, `CREATE TRIGGER reject_guest_issue_delete_audit BEFORE INSERT ON journal_entries WHEN NEW.summary='Guest deleted a project issue' BEGIN SELECT RAISE(ABORT,'synthetic audit failure');END`)
	call("delete-guest-a", "DELETE", base, body(current), 500)
	execOrFatal(t, db, `DROP TRIGGER reject_guest_issue_delete_audit`)
	if preview().Version != current.Version {
		t.Fatal("audit failure changed snapshot")
	}
	execOrFatal(t, db, `UPDATE missions SET status='IN_PROGRESS' WHERE id=?`, f.issue)
	working := preview()
	if working.CanDelete {
		t.Fatal("running issue offered deletion")
	}
	call("delete-guest-a", "DELETE", base, body(working), 409)
	execOrFatal(t, db, `UPDATE missions SET status='CANCELLED' WHERE id=?`, f.issue)
	cancelled := preview()
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE user_id='delete-guest-a'`)
	if preview().CanDelete {
		t.Fatal("narrow token offered deletion")
	}
	call("delete-guest-a", "DELETE", base, body(cancelled), 403)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes=NULL WHERE user_id='delete-guest-a'`)
	call("delete-guest-a", "DELETE", base, body(cancelled), 204)
	call("delete-guest-a", "GET", base+"/deletion", "", 404)
	var pointerCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id='guest-delete-assignment' AND mission_id IS NULL AND issue_execution_id IS NULL`).Scan(&pointerCount); err != nil || pointerCount != 1 {
		t.Fatal("historical assignment pointers not safely released", pointerCount, err)
	}
	var count int
	var actor string
	if err = db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id=?`, f.issue).Scan(&count); err != nil || count != 0 {
		t.Fatal("issue retained", count, err)
	}
	if err = db.QueryRow(`SELECT actor_id FROM agent_operations WHERE verb='deleted' AND entity_id=?`, f.issue).Scan(&actor); err != nil || actor != "delete-guest-a" {
		t.Fatal("wrong deletion actor", actor, err)
	}
	for _, chat := range []string{"delete-chat-one", "delete-chat-two"} {
		if rr := f.refs(chat); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"deleted":true`) || strings.Contains(rr.Body.String(), "MUST_NOT_LEAK") {
			t.Fatalf("trusted receipt %d %s", rr.Code, rr.Body.String())
		}
	}
	// A Guest cannot consume trusted tombstones or private operation results.
	call("delete-guest-a", "GET", "/api/v1/chats/delete-chat-one/entity-refs", "", 404)
}
