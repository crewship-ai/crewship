package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestAssignmentChatOriginAdmissionAndRevocation(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES('origin-worker',?,?,'Worker','origin-worker','IDLE')`, f.workspace, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('origin-busy',?,?,?,?,'busy','RUNNING',datetime('now'))`, f.workspace, f.chat, f.agent, f.agent)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	lock := chatbridge.NewAgentRunLock()
	lock.TryStart("origin-worker")
	h.SetAgentRunLock(lock)
	t.Cleanup(h.WaitDispatches)
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "chat_id": f.chat, "actor_agent_id": f.agent, "target_slug": "origin-worker", "task": "delegate work", "source_run_id": f.run}
	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
		h.WaitDispatches()
		return rr
	}
	rr := call()
	if rr.Code != 201 {
		t.Fatalf("admission %d %s", rr.Code, rr.Body.String())
	}
	var response struct {
		ID string `json:"assignment_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var origin, status string
	if err := f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,''),status FROM assignments WHERE id=?`, response.ID).Scan(&origin, &status); err != nil {
		t.Fatal(err)
	}
	if origin == "" || status != "QUEUED" {
		t.Fatalf("durable queued origin=%q status=%q", origin, status)
	}
	a, err := runauthority.ValidateOrigin(t.Context(), f.h.db, origin, "origin-worker", f.workspace)
	if err != nil || a.HumanAuthority == nil || a.HumanAuthority.UserID != f.user || a.ChatID != f.chat {
		t.Fatalf("child authority %+v %v", a, err)
	}
	// The parent may end while its durable delegated task waits in the queue.
	execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('origin-parent-ended',?,?,'run.completed','orchestrator','done',?)`, f.workspace, f.agent, f.run)
	if _, err := runauthority.ValidateOrigin(t.Context(), f.h.db, origin, "origin-worker", f.workspace); err != nil {
		t.Fatalf("parent end invalidated durable origin: %v", err)
	}
	// A fresh admission cannot cite an ended run, even though the existing child remains valid.
	if rr = call(); rr.Code != 403 {
		t.Fatalf("ended source admission %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	if _, err := runauthority.ValidateOrigin(t.Context(), f.h.db, origin, "origin-worker", f.workspace); err == nil {
		t.Fatal("revoked requester remained valid")
	}
}

func TestAssignmentChatOriginForgedSourceWritesNothing(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	t.Cleanup(h.WaitDispatches)
	var slug string
	if err := f.h.db.QueryRow(`SELECT slug FROM agents WHERE id=?`, f.agent).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "chat_id": f.chat, "actor_agent_id": f.agent, "target_slug": slug, "task": "forged", "source_run_id": "not-a-real-run"}
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	if rr.Code != 403 {
		t.Fatalf("forged source %d %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := f.h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM assignments)+(SELECT COUNT(*) FROM execution_origins)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("forged source wrote rows: %d %v", n, err)
	}
}

func TestDelayedMentionRecoversCommentChatOrigin(t *testing.T) {
	f := newChatIssueFixture(t)
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-DELAYED-ORIGIN", "TODO")
	execOrFatal(t, f.h.db, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,chat_origin_id) VALUES('delayed-origin-comment',?,'agent',?,'Please help',?)`, issue, f.agent, origin.ID)
	execOrFatal(t, f.h.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('mention-origin-busy',?,?,?,?,'busy','RUNNING',datetime('now'))`, f.workspace, f.chat, f.agent, f.agent)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	t.Cleanup(h.WaitDispatches)
	var target string
	if err := f.h.db.QueryRow(`SELECT id FROM agents WHERE crew_id=? AND id!=? LIMIT 1`, f.crew, f.agent).Scan(&target); err != nil {
		t.Fatal(err)
	}
	lock := chatbridge.NewAgentRunLock()
	lock.TryStart(target)
	h.SetAgentRunLock(lock)
	req := mentionDispatchRequest{WorkspaceID: f.workspace, MissionID: issue, Identifier: "ENG-DELAYED-ORIGIN", IssueTitle: "Origin", IssueCrewID: f.crew, CommentID: "delayed-origin-comment", CommentBody: "Please help", AuthorType: "agent", AuthorID: f.agent, TargetAgentID: target}
	id, err := h.DispatchMention(t.Context(), req)
	h.WaitDispatches()
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,'') FROM assignments WHERE id=?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != origin.ID {
		t.Fatalf("delayed mention origin=%q want %q", stored, origin.ID)
	}
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	if _, err := h.DispatchMention(t.Context(), req); err == nil {
		t.Fatal("revoked delayed mention admitted")
	}
}

func TestDelegatedChatRunKeepsMemberRequesterPermissions(t *testing.T) {
	ih, workspace, crew, lead, user := newInternalIssueHandler(t)
	execOrFatal(t, ih.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	seedChatIssueRun(t, ih, workspace, lead, user, "member-origin-chat", "member-origin-run")
	tx, err := ih.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, "member-origin-run", lead, workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, ih.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('member-parent-ended',?,?,'run.completed','orchestrator','done','member-origin-run')`, workspace, lead)
	child := "agent-worker"
	host := NewInternalHandler(ih.db, "host", newTestLogger())
	wireTestJournalForHandler(t, ih.db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(map[string]any{"id": "member-origin-child", "workspace_id": workspace, "agent_id": child, "trigger_type": "ASSIGNMENT", "chat_origin_id": origin.ID})))
	if rr.Code != 201 {
		t.Fatalf("host admitted child %d %s", rr.Code, rr.Body.String())
	}
	a, err := runauthority.Resolve(t.Context(), ih.db, "member-origin-child", child, workspace)
	if err != nil || a.HumanAuthority == nil || a.HumanAuthority.UserID != user {
		t.Fatalf("child became autonomous: %+v %v", a, err)
	}
	issue := seedIssue(t, ih.db, workspace, crew, lead, "ENG-MEMBER-CHILD", "TODO")
	rr = chatIssueCall(t, ih.UpdateStatus, "PATCH", "ENG-MEMBER-CHILD", map[string]any{"workspace_id": workspace, "crew_id": crew, "agent_id": child, "author_run_id": "member-origin-child", "operation_key": "member-child-update", "title": "forbidden"})
	if rr.Code != 403 {
		t.Fatalf("delegated child bypassed MEMBER update denial %d %s", rr.Code, rr.Body.String())
	}
	var title string
	if err := ih.db.QueryRow(`SELECT title FROM missions WHERE id=?`, issue).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title == "forbidden" {
		t.Fatal("delegation laundered issue mutation")
	}
}

func TestAssignmentIssueMutationSharesAuthorityTransaction(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-ASSIGN-TX", "TODO")
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, issue, "assignment-issue-source")
	execOrFatal(t, f.h.db, `CREATE TRIGGER reject_assignment_comment BEFORE INSERT ON mission_comments BEGIN SELECT RAISE(ABORT,'comment refused'); END`)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	t.Cleanup(h.WaitDispatches)
	var slug string
	if err := f.h.db.QueryRow(`SELECT slug FROM agents WHERE id='agent-worker'`).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "chat_id": issue, "actor_agent_id": f.agent, "target_slug": slug, "task": "work", "source_run_id": "assignment-issue-source"}
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	if rr.Code != 500 {
		t.Fatalf("failed comment admission %d %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := f.h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM assignments)+(SELECT COUNT(*) FROM execution_origins)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed admission wrote rows: %d %v", n, err)
	}
	var delegate string
	if err := f.h.db.QueryRow(`SELECT COALESCE(delegate_agent_id,'') FROM missions WHERE id=?`, issue).Scan(&delegate); err != nil {
		t.Fatal(err)
	}
	if delegate != "" {
		t.Fatalf("failed admission changed delegate %q", delegate)
	}
}

func TestAssignmentCompletionDoesNotPublishAfterRequesterRevocation(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-RESULT-ORIGIN", "TODO")
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, f.run, f.agent, f.workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,group_id,assigned_by_id,assigned_to_id,task,status,created_at,chat_origin_id) VALUES('result-origin-assignment',?,?,?,?,'agent-worker','work','RUNNING',datetime('now'),?)`, f.workspace, f.chat, issue, f.agent, origin.ID)
	execOrFatal(t, f.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	if !h.finishAssignment(t.Context(), "result-origin-assignment", "", f.chat, "worker", f.workspace, "Unapproved result text", "", nil) {
		t.Fatal("revoked assignment did not settle")
	}
	var status string
	if err := f.h.db.QueryRow(`SELECT status FROM assignments WHERE id='result-origin-assignment'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" {
		t.Fatalf("revoked assignment stuck %s", status)
	}
	chatIssueCount(t, f, "mission_comments", 0)
}
