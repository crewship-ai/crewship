package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueWork_TransferReceiptAndFence(t *testing.T) {
	h, user, ws, crew, lead, agent := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "TODO")
	seedMissionAssignment(t, h, ws, id, agent, "finished-history", "COMPLETED", "Verified result", "", "")
	if _, err := h.db.ExecContext(t.Context(), `UPDATE assignments SET mission_id=? WHERE id='finished-history'`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE missions SET delegate_agent_id=?,owner_user_id=? WHERE id=?`, agent, user, id); err != nil {
		t.Fatal(err)
	}
	send := func(op, action, target, note string, revision int) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"operation_id": op, "action": action, "target_id": target, "note": note, "revision": revision})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		h.Work(rr, req)
		return rr
	}
	for i := 0; i < 2; i++ {
		rr := send("take-1", "take_over", "", "I will check the result", 0)
		if rr.Code != 200 {
			t.Fatalf("take %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	var mode, worker, owner, delegate string
	var revision, events int
	if err := h.db.QueryRow(`SELECT w.mode,w.worker_user_id,w.revision,m.owner_user_id,m.delegate_agent_id FROM issue_work w JOIN missions m ON m.id=w.mission_id WHERE m.id=?`, id).Scan(&mode, &worker, &revision, &owner, &delegate); err != nil {
		t.Fatal(err)
	}
	if mode != "human" || worker != user || revision != 1 || owner != user || delegate != agent {
		t.Fatalf("bad work state: %s %s %d %s %s", mode, worker, revision, owner, delegate)
	}
	h.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id=? AND source_kind='work_operation'`, id).Scan(&events)
	if events != 1 {
		t.Fatalf("receipt duplicated: %d", events)
	}
	if rr := send("take-1", "take_over", "", "different", 0); rr.Code != 409 {
		t.Fatalf("different replay = %d", rr.Code)
	}
	if rr := send("stale", "handoff_agent", agent, "Continue", 0); rr.Code != 409 {
		t.Fatalf("stale write = %d", rr.Code)
	}
	// The fence is in the database, so CLI, mentions and recovery cannot bypass it.
	_, err := h.db.Exec(`INSERT INTO assignments(id,workspace_id,mission_id,assigned_to_id,task,status) VALUES('held-run',?,?,?,'work','PENDING')`, ws, id, agent)
	if err == nil || !strings.Contains(err.Error(), "held by a human") {
		t.Fatalf("expected work fence, got %v", err)
	}
	if rr := send("submit", "submit", "", "Checked the output; ready for review", 1); rr.Code != 200 {
		t.Fatalf("submit = %d %s", rr.Code, rr.Body.String())
	}
	if rr := send("return", "handoff_agent", agent, "Apply review feedback; preserve the checked files", 2); rr.Code != 200 {
		t.Fatalf("handback = %d %s", rr.Code, rr.Body.String())
	}
	h.db.QueryRow(`SELECT mode,revision FROM issue_work WHERE mission_id=?`, id).Scan(&mode, &revision)
	if mode != "agent" || revision != 3 {
		t.Fatalf("handback: %s %d", mode, revision)
	}
}

func TestIssueWorkRejectsChangedBriefAndForeignWorker(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "TODO")
	if _, err := h.db.Exec(`UPDATE missions SET description='New requirements' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	send := func(rev int, target string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"operation_id": "handoff", "action": "handoff_human", "target_id": target, "note": "Review the changed requirements", "revision": rev})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
		rr := httptest.NewRecorder()
		h.Work(rr, req)
		return rr
	}
	if rr := send(0, user); rr.Code != 409 {
		t.Fatalf("stale brief allowed: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(1, "unknown-user"); rr.Code != 400 {
		t.Fatalf("foreign worker allowed: %d %s", rr.Code, rr.Body.String())
	}
	var revision int
	h.db.QueryRow(`SELECT revision FROM issue_work WHERE mission_id=?`, id).Scan(&revision)
	if revision != 1 {
		t.Fatalf("rejected operation changed revision to %d", revision)
	}
}

func TestIssueWorkHumanReplyResumesWaitingPlanStep(t *testing.T) {
	h, user, ws, crew, lead, agent := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "IN_PROGRESS")
	seedMissionAssignment(t, h, ws, id, agent, "needs-input", "COMPLETED", "What budget should I use?", "", "")
	if _, err := h.db.Exec(`UPDATE assignments SET outcome='NEEDS_HUMAN',mission_id=? WHERE id='needs-input'`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE mission_tasks SET status='AWAITING_APPROVAL' WHERE assignment_id='needs-input'`); err != nil {
		t.Fatal(err)
	}
	seedMissionAssignment(t, h, ws, id, agent, "other-input", "COMPLETED", "A separate question", "", "")
	if _, err := h.db.Exec(`UPDATE assignments SET outcome='NEEDS_HUMAN',mission_id=? WHERE id='other-input'`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE mission_tasks SET status='AWAITING_APPROVAL' WHERE assignment_id='other-input'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`INSERT INTO assignments(id,workspace_id,mission_id,assigned_by_id,assigned_to_id,created_by_user_id,chat_id,task,status) VALUES('answer-run',?,?,?,?,?,'chat-needs-input','Use the approved budget','PENDING')`, ws, id, lead, agent, user); err != nil {
		t.Fatal(err)
	}
	var assignment, status string
	if err := h.db.QueryRow(`SELECT assignment_id,status FROM mission_tasks WHERE id='mt-needs-input'`).Scan(&assignment, &status); err != nil {
		t.Fatal(err)
	}
	var unrelated string
	if err := h.db.QueryRow(`SELECT assignment_id FROM mission_tasks WHERE id='mt-other-input'`).Scan(&unrelated); err != nil || unrelated != "other-input" {
		t.Fatalf("answer resumed an unrelated waiting step: %s %v", unrelated, err)
	}
	if assignment != "answer-run" || status != "IN_PROGRESS" {
		t.Fatalf("reply left original task stranded: %s %s", assignment, status)
	}
}

func TestIssueCommentsCursorBoundsLargeThread(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "TODO")
	if _, err := h.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000)
 INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at)
 SELECT printf('comment-%05d',x),?,'user',?,'Progress update','2026-09-07T00:00:00Z','2026-09-07T00:00:00Z' FROM n`, id, user); err != nil {
		t.Fatal(err)
	}
	fetch := func(before string) []commentResponse {
		t.Helper()
		req := issueRunsRequest(t, user, ws, crew, "ENG-1")
		q := req.URL.Query()
		q.Set("page_size", "100")
		q.Set("before_id", before)
		req.URL.RawQuery = q.Encode()
		rr := httptest.NewRecorder()
		h.ListComments(rr, req)
		if rr.Code != 200 {
			t.Fatalf("comments %d %s", rr.Code, rr.Body.String())
		}
		var rows []commentResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 100 || rr.Header().Get("X-Has-More") != "true" {
			t.Fatalf("unbounded or incomplete page: %d", len(rows))
		}
		return rows
	}
	first := fetch("")
	if first[0].ID != "comment-09901" || first[99].ID != "comment-10000" {
		t.Fatal("latest comments not in stable chronological order")
	}
	if _, err := h.db.Exec(`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at) VALUES('new-comment',?,'user',?,'New update','2026-09-07T01:00:00Z','2026-09-07T01:00:00Z')`, id, user); err != nil {
		t.Fatal(err)
	}
	second := fetch(first[0].ID)
	if second[0].ID != "comment-09801" || second[99].ID != "comment-09900" {
		t.Fatal("concurrent new comment shifted the cursor page")
	}
}

func TestInternalIssueWorkCannotTakeHumanWork(t *testing.T) {
	h, ws, crew, agent, user := newInternalIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, agent, "ENG-1", "TODO")
	if _, err := h.db.Exec(`UPDATE missions SET delegate_agent_id=? WHERE id=?`, agent, id); err != nil {
		t.Fatal(err)
	}
	send := func(action string, revision int) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"workspace_id": ws, "agent_id": agent, "action": action, "revision": revision, "operation_id": action, "target_id": user, "note": "Please verify the deliverable"})
		req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		req.SetPathValue("identifier", "ENG-1")
		rr := httptest.NewRecorder()
		h.Work(rr, req)
		return rr
	}
	if rr := send("take_over", 0); rr.Code != 403 {
		t.Fatalf("agent impersonated a person: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send("handoff_human", 0); rr.Code != 200 {
		t.Fatalf("agent could not request human work: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send("handoff_human", 0); rr.Code != 200 {
		t.Fatalf("handoff replay failed: %d", rr.Code)
	}
	var notices int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id=? AND target_user_id=? AND json_extract(payload_json,'$.issue_identifier')='ENG-1'`, ws, user).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("handoff must notify its recipient exactly once: %d %v", notices, err)
	}
	if rr := send("handoff_agent", 1); rr.Code != 403 {
		t.Fatalf("agent stole human work: %d %s", rr.Code, rr.Body.String())
	}
	var authorType, authorID string
	if err := h.db.QueryRow(`SELECT author_type,author_id FROM mission_comments WHERE mission_id=?`, id).Scan(&authorType, &authorID); err != nil {
		t.Fatal(err)
	}
	if authorType != "agent" || authorID != agent {
		t.Fatalf("incorrect handoff author: %s %s", authorType, authorID)
	}
}

func TestIssueWorkRejectsReplyWithMultipleWaitingTasks(t *testing.T) {
	h, user, ws, crew, lead, agent := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "IN_PROGRESS")
	for _, run := range []string{"first-input", "second-input"} {
		seedMissionAssignment(t, h, ws, id, agent, run, "COMPLETED", "Question", "", "")
		if _, err := h.db.ExecContext(t.Context(), `UPDATE assignments SET outcome='NEEDS_HUMAN',mission_id=?,chat_id='chat-first-input' WHERE id=?`, id, run); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.ExecContext(t.Context(), `UPDATE mission_tasks SET status='AWAITING_APPROVAL' WHERE assignment_id=?`, run); err != nil {
			t.Fatal(err)
		}
	}
	_, err := h.db.ExecContext(t.Context(), `INSERT INTO assignments(id,workspace_id,mission_id,assigned_by_id,assigned_to_id,created_by_user_id,chat_id,task,status) VALUES('ambiguous-answer',?,?,?,?,?,'chat-first-input','Answer','PENDING')`, ws, id, lead, agent, user)
	if err == nil || !strings.Contains(err.Error(), "multiple waiting tasks") {
		t.Fatalf("ambiguous reply was accepted: %v", err)
	}
	var waiting, inserted int
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM mission_tasks WHERE mission_id=? AND status='AWAITING_APPROVAL'`, id).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assignments WHERE id='ambiguous-answer'`).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	if waiting != 2 || inserted != 0 {
		t.Fatalf("partial resume: waiting=%d inserted=%d", waiting, inserted)
	}
}
