package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestGuestIssueContributeIsProjectScopedAndAudited(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	crew := seedTestCrew(t, db, ws)
	seedAgentRow(t, db, "INTAKE_AGENT_PRIVATE", ws, crew, "Private intake lead", "intake-lead", "LEAD")
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('contributor','contributor@example.test'),('reader','reader@example.test')`)
	for _, user := range []string{"contributor", "reader"} {
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user+"-member", ws, user)
	}
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('client-project',?,'Client project','client-project'),('OTHER_PROJECT_PRIVATE',?,'Other client','other-client')`, ws, ws)
	store := access.Store{DB: db}
	for _, user := range []string{"contributor", "reader"} {
		member, err := store.Membership(t.Context(), user, ws)
		if err != nil {
			t.Fatal(err)
		}
		rights := []access.Right{{Kind: "project", ID: "client-project", Operation: "list"}, {Kind: "project", ID: "client-project", Operation: "read"}}
		if user == "contributor" {
			rights = append(rights, access.Right{Kind: "project", ID: "client-project", Operation: "contribute"})
		}
		if _, err = store.Replace(t.Context(), owner, user, ws, "restricted", member, rights); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{})
	defer writer.Close()
	router, err := NewRouter(db, "guest-contribute-secret-long-enough", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{}
	for _, user := range []string{owner, "contributor", "reader"} {
		tokens[user] = mintTokenFor(t, db, user, user+"-"+strings.Repeat("0", 40))
	}
	call := func(user, method, path, body string, want int, keys ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", ws)
		if len(keys) > 0 {
			req.Header.Set("Idempotency-Key", keys[0])
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s %s = %d want %d: %s", user, method, path, rr.Code, want, rr.Body.String())
		}
		for _, secret := range []string{"INTAKE_AGENT_PRIVATE", "Private intake lead", "OTHER_PROJECT_PRIVATE", owner + "@"} {
			if user != owner && strings.Contains(rr.Body.String(), secret) {
				t.Fatalf("guest response leaked %s: %s", secret, rr.Body.String())
			}
		}
		return rr
	}
	base := "/api/v1/workspaces/" + ws + "/restricted-issues"
	call("contributor", "GET", "/api/v1/workspaces/not-selected/restricted-issue-projects?workspace_id="+ws, "", 404)
	call("contributor", "GET", "/api/v1/workspaces/not-selected/restricted-issues?workspace_id="+ws, "", 404)
	call("contributor", "POST", "/api/v1/workspaces/not-selected/restricted-issues?workspace_id="+ws, `{"project_id":"client-project","title":"Wrong workspace path"}`, 404)
	intake := "/api/v1/workspaces/" + ws + "/projects/client-project/guest-intake"
	contributor, err := store.Membership(t.Context(), "contributor", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "contributor", ws, "restricted", contributor, []access.Right{{Kind: "project", ID: "client-project", Operation: "contribute"}}); err != nil {
		t.Fatal(err)
	}
	call("contributor", "POST", base, `{"project_id":"client-project","title":"Contribute without Read"}`, 404)
	explanation := call("contributor", "GET", "/api/v1/workspaces/"+ws+"/access/me", "", 200)
	if !strings.Contains(explanation.Body.String(), "requires a current Read grant") || !strings.Contains(explanation.Body.String(), `"state":"denied"`) {
		t.Fatal("misleading contribution explanation", explanation.Body.String())
	}
	contributor, err = store.Membership(t.Context(), "contributor", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "contributor", ws, "restricted", contributor, []access.Right{{Kind: "project", ID: "client-project", Operation: "list"}, {Kind: "project", ID: "client-project", Operation: "read"}, {Kind: "project", ID: "client-project", Operation: "contribute"}}); err != nil {
		t.Fatal(err)
	}
	call("contributor", "POST", base, `{"project_id":"client-project","title":"Client request"}`, 409)
	call(owner, "PUT", intake, `{"crew_id":"`+crew+`","revision":0}`, 200)
	call(owner, "PUT", intake, `{"crew_id":"`+crew+`","revision":0}`, 409)
	call("contributor", "GET", intake, "", 404)
	call("reader", "POST", base, `{"project_id":"client-project","title":"Not allowed"}`, 404)
	call("contributor", "POST", base, `{"project_id":"OTHER_PROJECT_PRIVATE","title":"Wrong client"}`, 404)
	created := call("contributor", "POST", base, `{"project_id":"client-project","title":"Client request","description":"Client brief"}`, 201, "create-request")
	replay := call("contributor", "POST", base, `{"project_id":"client-project","title":"Client request","description":"Client brief"}`, 201, "create-request")
	if replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != created.Body.String() {
		t.Fatal("creation replay changed result")
	}
	call("contributor", "POST", base, `{"project_id":"client-project","title":"Different request"}`, 409, "create-request")
	var issue restrictedIssue
	if err = json.Unmarshal(created.Body.Bytes(), &issue); err != nil || issue.ID == "" || issue.Title != "Client request" {
		t.Fatalf("created projection: %+v %v", issue, err)
	}
	var creator, assigned, status string
	if err = db.QueryRow(`SELECT created_by_user_id,COALESCE(delegate_agent_id,''),status FROM missions WHERE id=?`, issue.ID).Scan(&creator, &assigned, &status); err != nil || creator != "contributor" || assigned != "" || status != "BACKLOG" {
		t.Fatalf("creation provenance/dispatch: %q %q %q %v", creator, assigned, status, err)
	}
	var origin string
	if err = db.QueryRow(`SELECT chat_origin_id FROM missions WHERE id=?`, issue.ID).Scan(&origin); err != nil || origin == "" {
		t.Fatalf("missing human origin %q %v", origin, err)
	}
	if _, err = runauthority.ValidateOrigin(t.Context(), db, origin, "INTAKE_AGENT_PRIVATE", ws); err == nil {
		t.Fatal("Guest intake acquired execution authority")
	}
	path := base + "/" + issue.ID
	call("reader", "POST", path+"/comments", `{"project_id":"client-project","body":"Read is not write"}`, 404)
	comment := call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"Client question"}`, 201, "comment-request")
	replay = call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"Client question"}`, 201, "comment-request")
	if replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != comment.Body.String() {
		t.Fatal("comment replay changed result")
	}
	var commentOrigin string
	if err = db.QueryRow(`SELECT chat_origin_id FROM mission_comments WHERE mission_id=? AND body='Client question'`, issue.ID).Scan(&commentOrigin); err != nil || commentOrigin == "" {
		t.Fatalf("missing comment origin %q %v", commentOrigin, err)
	}
	if _, err = runauthority.ValidateOrigin(t.Context(), db, commentOrigin, "INTAKE_AGENT_PRIVATE", ws); err == nil {
		t.Fatal("Guest comment acquired execution authority")
	}
	if !strings.Contains(comment.Body.String(), "Client question") {
		t.Fatal(comment.Body.String())
	}
	execOrFatal(t, db, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body) VALUES('private-comment',?,'user',?,'STAFF_PRIVATE_COMMENT_CANARY')`, issue.ID, owner)
	thread := call("contributor", "GET", path+"/comments", "", 200)
	if strings.Contains(thread.Body.String(), "STAFF_PRIVATE_COMMENT_CANARY") || !strings.Contains(thread.Body.String(), "Client question") {
		t.Fatal("comment visibility", thread.Body.String())
	}
	trustedComments := "/api/v1/crews/" + crew + "/issues/" + issue.Identifier + "/comments"
	call(owner, "POST", trustedComments, `{"expected_project_id":"client-project","body":"[@lead](crewship:agent/INTAKE_AGENT_PRIVATE)","guest_visible":true}`, 400)
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"[@lead](crewship:agent/INTAKE_AGENT_PRIVATE)"}`, 400)
	call(owner, "POST", trustedComments, `{"expected_project_id":"client-project","body":"STAFF_DEFAULT_PRIVATE_CANARY"}`, 201)
	call(owner, "POST", trustedComments, `{"expected_project_id":"client-project","body":"Shared staff answer","guest_visible":true}`, 201)
	thread = call("contributor", "GET", path+"/comments", "", 200)
	if !strings.Contains(thread.Body.String(), "Shared staff answer") || strings.Contains(thread.Body.String(), "STAFF_DEFAULT_PRIVATE_CANARY") {
		t.Fatal("explicit staff sharing", thread.Body.String())
	}
	execOrFatal(t, db, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<105) INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,guest_visible,guest_project_id,created_at,updated_at) SELECT 'older-shared-'||x,?,'user',?,'Older shared comment',1,'client-project','2026-10-13T00:00:00Z','2026-10-13T00:00:00Z' FROM n`, issue.ID, owner)
	thread = call("contributor", "GET", path+"/comments", "", 200)
	var visible []guestIssueComment
	if err = json.Unmarshal(thread.Body.Bytes(), &visible); err != nil || len(visible) != 100 || !strings.Contains(thread.Body.String(), "Client question") || !strings.Contains(thread.Body.String(), "Shared staff answer") {
		t.Fatalf("bounded thread lost latest comments: %d %v %s", len(visible), err, thread.Body.String())
	}
	// A journal outage does not lose the committed activity or retry intent.
	// Projection recovery uses one stable journal ID and emits no duplicate.
	router.journal = unavailableIssueJournal{}
	outage := call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"DURABLE_DURING_OUTAGE"}`, 201, "outage-request")
	if outage.Header().Get("Operation-ID") == "" {
		t.Fatal("missing operation receipt")
	}
	var pending, projected int
	if err = db.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE mission_id=? AND state='pending'`, issue.ID).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("lost pending journal %d %v", pending, err)
	}
	router.journal = writer
	execOrFatal(t, db, `UPDATE issue_mutation_outbox SET next_attempt_at='2000-01-01T00:00:00Z' WHERE mission_id=? AND state='pending'`, issue.ID)
	delivery := issueEvents{db: db, logger: newTestLogger(), journal: writer}.mutationDelivery()
	delivery.drainCommittedIssueMutation(ws)
	delivery.drainCommittedIssueMutation(ws)
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE mission_id=? AND entry_type='mission.comment'`, issue.ID).Scan(&projected); err != nil || projected != 4 {
		t.Fatalf("missing/duplicate recovered journal %d %v", projected, err)
	}
	// Explicit mutation scopes narrow action hints and still gate writes.
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes='["agents:write"]' WHERE id='clt-contributor'`)
	readonly := call("contributor", "GET", path, "", 200)
	if !strings.Contains(readonly.Body.String(), `"can_contribute":false`) {
		t.Fatal("Token without workspace mutation scope offered contribution", readonly.Body.String())
	}
	call("contributor", "GET", path+"/comments", "", 200)
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"Narrow token cannot contribute"}`, 403)
	execOrFatal(t, db, `UPDATE cli_tokens SET scopes=NULL WHERE id='clt-contributor'`)
	// Audit/outbox insertion is part of both comment transactions.
	execOrFatal(t, db, `CREATE TRIGGER deny_guest_comment_audit BEFORE INSERT ON issue_mutation_outbox WHEN NEW.kind='journal' BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"MUST_ROLL_BACK_COMMENT"}`, 500)
	call(owner, "POST", trustedComments, `{"expected_project_id":"client-project","body":"STAFF_MUST_ROLL_BACK_COMMENT","guest_visible":true}`, 500)
	var rolledBack int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE body IN ('MUST_ROLL_BACK_COMMENT','STAFF_MUST_ROLL_BACK_COMMENT')`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("comment escaped audit transaction: %d %v", rolledBack, err)
	}
	execOrFatal(t, db, `DROP TRIGGER deny_guest_comment_audit`)
	call("contributor", "PATCH", path, `{"status":"TODO"}`, 200)
	execOrFatal(t, db, `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,stage,reviewer_agent_id,created_at,updated_at) VALUES('active-execution',?,0,0,'working','INTAKE_AGENT_PRIVATE','2026-10-14T00:00:00Z','2026-10-14T00:00:00Z')`, issue.ID)
	call("contributor", "PATCH", path, `{"status":"BACKLOG"}`, 409)
	execOrFatal(t, db, `DELETE FROM issue_executions WHERE id='active-execution'`)
	seedMissionAssignment(t, &IssueHandler{db: db}, ws, issue.ID, "INTAKE_AGENT_PRIVATE", "guest-contribute-active-task", "PENDING", "", "", "")
	call("contributor", "PATCH", path, `{"status":"BACKLOG"}`, 409)
	execOrFatal(t, db, `UPDATE assignments SET status='COMPLETED' WHERE id='guest-contribute-active-task'`)
	execOrFatal(t, db, `UPDATE mission_tasks SET status='COMPLETED' WHERE assignment_id='guest-contribute-active-task'`)
	call("contributor", "PATCH", path, `{"status":"BACKLOG"}`, 200)
	call("contributor", "PATCH", path, `{"project_id":"OTHER_PROJECT_PRIVATE"}`, 400)
	call("contributor", "DELETE", path, fmt.Sprintf(`{"project_id":%q,"version":%q,"confirm":true}`, issue.ProjectID, strings.Repeat("a", 64)), 404)
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE actor_id='contributor' AND mission_id=?`, issue.ID).Scan(&n); err != nil || n < 3 {
		t.Fatalf("missing atomic audit: %d %v", n, err)
	}
	member, err := store.Membership(t.Context(), "contributor", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "contributor", ws, "restricted", member, []access.Right{{Kind: "project", ID: "client-project", Operation: "list"}, {Kind: "project", ID: "client-project", Operation: "read"}}); err != nil {
		t.Fatal(err)
	}
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"Revoked write"}`, 404)
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"Client question"}`, 404, "comment-request")
	call("contributor", "GET", path, "", 200)
	// A contributor entitled to both projects still must explicitly approve
	// the new audience; neither a Guest nor a trusted staff draft follows a move.
	member, err = store.Membership(t.Context(), "contributor", ws)
	if err != nil {
		t.Fatal(err)
	}
	var both []access.Right
	for _, project := range []string{"client-project", "OTHER_PROJECT_PRIVATE"} {
		for _, op := range []string{"read", "contribute"} {
			both = append(both, access.Right{Kind: "project", ID: project, Operation: op})
		}
	}
	if _, err = store.Replace(t.Context(), owner, "contributor", ws, "restricted", member, both); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE missions SET project_id='OTHER_PROJECT_PRIVATE' WHERE id=?`, issue.ID)
	call("contributor", "POST", path+"/comments", `{"project_id":"client-project","body":"STALE_CLIENT_AUDIENCE"}`, 409)
	call(owner, "POST", trustedComments, `{"expected_project_id":"client-project","guest_visible":true,"body":"STALE_STAFF_AUDIENCE"}`, 409)
	execOrFatal(t, db, `UPDATE missions SET project_id='client-project' WHERE id=?`, issue.ID)
	// A trusted move does not share the original client's thread with a
	// different client. Visibility is bound to the project at publication.
	member, err = store.Membership(t.Context(), "reader", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "reader", ws, "restricted", member, []access.Right{{Kind: "project", ID: "OTHER_PROJECT_PRIVATE", Operation: "read"}}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE missions SET project_id='OTHER_PROJECT_PRIVATE' WHERE id=?`, issue.ID)
	// This reader is now explicitly entitled to the other project's id.
	req := httptest.NewRequest("GET", path+"/comments", nil)
	req.Header.Set("Authorization", "Bearer "+tokens["reader"])
	req.Header.Set("X-Workspace-ID", ws)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "Client question") || strings.Contains(rr.Body.String(), "Shared staff answer") {
		t.Fatalf("cross-project shared thread leaked: %d %s", rr.Code, rr.Body.String())
	}
}
