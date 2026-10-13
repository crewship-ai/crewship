package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func captureReviewOrigin(t *testing.T, h *InternalIssueHandler, ws, agent, run string) string {
	t.Helper()
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	origin, err := runauthority.CaptureOrigin(t.Context(), tx, run, agent, ws)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return origin.ID
}

func TestDelegationRejectsBootIssueAndDerivesVerifiedExecutionChat(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-BOOT", "TODO")
	if err := ensureMissionChat(t.Context(), f.h.db, issue, f.workspace, f.agent, "Boot issue"); err != nil {
		t.Fatal(err)
	}
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	t.Cleanup(h.WaitDispatches)
	lock := chatbridge.NewAgentRunLock()
	lock.TryStart("agent-worker")
	h.SetAgentRunLock(lock)
	execOrFatal(t, f.h.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('review-busy',?,?,?,?,'busy','RUNNING',datetime('now'))`, f.workspace, f.chat, f.agent, f.agent)
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "actor_agent_id": f.agent, "source_run_id": f.run, "target_slug": "worker", "task": "Current user task", "chat_id": issue}
	call := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
		h.WaitDispatches()
		return rr
	}
	if rr := call(); rr.Code != 403 {
		t.Fatalf("boot issue admitted %d %s", rr.Code, rr.Body.String())
	}
	chatIssueCount(t, f, "mission_comments", 0)
	body["chat_id"] = ""
	if rr := call(); rr.Code != 201 {
		t.Fatalf("host chat derivation %d %s", rr.Code, rr.Body.String())
	}
	var actual string
	if err := f.h.db.QueryRow(`SELECT chat_id FROM assignments WHERE task='Current user task'`).Scan(&actual); err != nil || actual != f.chat {
		t.Fatalf("wrong execution chat %q %v", actual, err)
	}
	var delegate string
	if err := f.h.db.QueryRow(`SELECT COALESCE(delegate_agent_id,'') FROM missions WHERE id=?`, issue).Scan(&delegate); err != nil || delegate != "" {
		t.Fatalf("boot issue mutated %q %v", delegate, err)
	}
}

func TestQueryUsesVerifiedActorIDWithoutRosterSlug(t *testing.T) {
	f := newChatIssueFixture(t)
	h := NewQueryHandler(f.h.db, nil, nil, "host", newTestLogger())
	host := NewInternalHandler(f.h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, f.h.db, host)
	h.SetJournal(host.journal)
	body := map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "actor_agent_id": f.agent, "source_run_id": f.run, "target_slug": "worker", "question": "Question", "from_slug": "", "chat_id": ""}
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(body)))
	if rr.Code != 503 {
		t.Fatalf("query failed before real runtime boundary %d %s", rr.Code, rr.Body.String())
	}
	var actor, chat string
	if err := f.h.db.QueryRow(`SELECT from_agent_id,chat_id FROM peer_conversations`).Scan(&actor, &chat); err != nil || actor != f.agent || chat != f.chat {
		t.Fatalf("wrong query identity %q %q %v", actor, chat, err)
	}
}

func TestHumanAssignmentWithoutOriginStampsMissingRequester(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_by_user_id,created_at) VALUES('human-channel-assignment',?,?,?,'agent-worker','Channel question','PENDING',?,datetime('now'))`, f.workspace, f.chat, f.agent, f.user)
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	rec := &recordingEmitter{}
	h.SetJournal(rec)
	h.runAssignment(t.Context(), "human-channel-assignment", createAssignmentBody{WorkspaceID: f.workspace, CrewID: f.crew, ChatID: f.chat, TargetSlug: "worker", Task: "Channel question"}, targetAgentInfo{ID: "agent-worker", Slug: "worker", Name: "Worker"})
	started := findRunStartedEntry(t, rec, "human-channel-assignment")
	if started.Payload["requester_authority_missing"] != true || started.Payload["created_by_user_id"] != f.user {
		t.Fatalf("human request became autonomous: %+v", started.Payload)
	}
}

func TestMissionStartCannotReplacePersistedHumanOrigin(t *testing.T) {
	f := newChatIssueFixture(t)
	first := captureReviewOrigin(t, f.h, f.workspace, f.agent, f.run)
	seedChatIssueRun(t, f.h, f.workspace, f.agent, f.user, "different-source-chat", "different-source-run")
	execOrFatal(t, f.h.db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,title,trace_id,status,mission_type,chat_origin_id) VALUES('review-origin-mission',?,?,?,'Mission','review-mission-trace','PLANNING','orchestration',?)`, f.workspace, f.crew, f.agent, first)
	h := NewInternalMissionHandler(f.h.db, nil, nil, newTestLogger())
	call := func(run string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/start?workspace_id="+f.workspace+"&crew_id="+f.crew+"&actor_agent_id="+f.agent+"&source_run_id="+run, nil)
		req.SetPathValue("missionId", "review-origin-mission")
		rr := httptest.NewRecorder()
		h.Start(rr, req)
		return rr
	}
	if rr := call("different-source-run"); rr.Code != 403 {
		t.Fatalf("mission rebound %d %s", rr.Code, rr.Body.String())
	}
	var origin, status string
	f.h.db.QueryRow(`SELECT chat_origin_id,status FROM missions WHERE id='review-origin-mission'`).Scan(&origin, &status)
	if origin != first || status != "PLANNING" {
		t.Fatalf("provenance mutated %q %s", origin, status)
	}
	if rr := call(f.run); rr.Code != 200 {
		t.Fatalf("same-origin legitimate start refused %d %s", rr.Code, rr.Body.String())
	}
}

func TestQueuedFollowUpsKeepEachCommentsOwnOrigin(t *testing.T) {
	for _, humanFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(humanFirst), func(t *testing.T) {
			f := setupMentionFixture(t)
			if err := ensureMissionChat(t.Context(), f.db, f.missionID, f.wsID, f.target, "Issue"); err != nil {
				t.Fatal(err)
			}
			seedChatIssueRun(t, f.internal, f.wsID, f.author, f.userID, "followup-root-chat", "followup-root-run")
			origin := captureReviewOrigin(t, f.internal, f.wsID, f.author, "followup-root-run")
			seedFollowUpFixture(t, f, "authority-group", "finished-group", "COMPLETED", 2)
			for i := 0; i < 2; i++ {
				o := ""
				if (i == 0) == humanFirst {
					o = origin
				}
				execOrFatal(t, f.db, `UPDATE mission_comments SET author_type='agent',author_id=?,chat_origin_id=NULLIF(?,'') WHERE id=?`, f.author, o, fmt.Sprintf("cmt_fu_authority-group_%d", i))
			}
			lock := chatbridge.NewAgentRunLock()
			lock.TryStart(f.target)
			f.assign.SetAgentRunLock(lock)
			execOrFatal(t, f.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crewID)
			execOrFatal(t, f.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('followup-busy',?,?,?,?, 'busy','RUNNING',datetime('now'))`, f.wsID, f.missionID, f.author, f.author)
			// The finishing run deliberately carries the opposite authority.
			inherited := ""
			if !humanFirst {
				inherited = origin
			}
			f.assign.dispatchQueuedFollowUpsForSession(runauthority.WithOriginID(context.Background(), inherited), "finished-group", f.wsID)
			f.assign.WaitDispatches()
			var actual, task string
			if err := f.db.QueryRow(`SELECT COALESCE(chat_origin_id,''),task FROM assignments WHERE session_id='authority-group' AND id!='finished-group'`).Scan(&actual, &task); err != nil {
				t.Fatal(err)
			}
			want := ""
			if humanFirst {
				want = origin
			}
			if actual != want || !strings.Contains(task, "comment number 0") || strings.Contains(task, "comment number 1") {
				t.Fatalf("mixed authority origin=%q want=%q task=%s", actual, want, task)
			}
		})
	}
}

func TestChannelJobAdmissionRetainsHumanMutationFence(t *testing.T) {
	h, store, conversation, user, agent, _ := conversationJobFixture(t)
	job := enqueueConversationJob(t, h, store, conversation, user, agent, "requester-origin-review")
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, conversation.WorkspaceID, user)
	if _, err := h.prepareConversationJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	var assignment string
	if err := h.db.QueryRow(`SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE id=?`, job).Scan(&assignment); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingEmitter{}
	h.SetJournal(recorder)
	if err := h.dispatchByID(t.Context(), assignment); err != nil {
		t.Fatal(err)
	}
	h.WaitDispatches()
	started := findRunStartedEntry(t, recorder, assignment)
	if started.Payload["requester_authority_missing"] != true || started.Payload["created_by_user_id"] != user {
		t.Fatalf("real channel requester lost: %+v", started.Payload)
	}
}

func TestDelegationExecutionChatDiffersFromRootHumanAnchor(t *testing.T) {
	f := newChatIssueFixture(t)
	origin := captureReviewOrigin(t, f.h, f.workspace, f.agent, f.run)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-CHILD-CONTEXT", "TODO")
	if err := ensureMissionChat(t.Context(), f.h.db, issue, f.workspace, f.agent, "Child issue"); err != nil {
		t.Fatal(err)
	}
	host := NewInternalHandler(f.h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, f.h.db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(map[string]any{"id": "review-child-run", "workspace_id": f.workspace, "agent_id": f.agent, "chat_id": issue, "chat_origin_id": origin, "trigger_type": "ASSIGNMENT"})))
	if rr.Code != 201 {
		t.Fatalf("admit child %d %s", rr.Code, rr.Body.String())
	}
	authority, err := runauthority.Resolve(t.Context(), f.h.db, "review-child-run", f.agent, f.workspace)
	if err != nil || authority.ChatID != f.chat {
		t.Fatalf("root anchor %+v %v", authority, err)
	}
	chat, err := delegationExecutionChat(t.Context(), f.h.db, "review-child-run", f.agent, f.workspace)
	if err != nil || chat != issue {
		t.Fatalf("borrowed root anchor instead of execution chat: %s %v", chat, err)
	}
}

func TestAssignmentCompletionWithoutHumanAuthorityDoesNotPublish(t *testing.T) {
	for _, human := range []bool{true, false} {
		t.Run(fmt.Sprint(human), func(t *testing.T) {
			f := newChatIssueFixture(t)
			issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-MISSING-RESULT", "TODO")
			requester := ""
			wantComments := 1
			if human {
				requester = f.user
				wantComments = 0
			}
			execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,group_id,assigned_by_id,assigned_to_id,task,status,created_at,created_by_user_id) VALUES('result-missing-authority',?,?,?,?,'agent-worker','work','RUNNING',datetime('now'),NULLIF(?,''))`, f.workspace, f.chat, issue, f.agent, requester)
			h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
			if !h.finishAssignment(t.Context(), "result-missing-authority", "", f.chat, "worker", f.workspace, "Unapproved result", "", nil) {
				t.Fatal("assignment did not settle")
			}
			var status string
			if err := f.h.db.QueryRow(`SELECT status FROM assignments WHERE id='result-missing-authority'`).Scan(&status); err != nil || status != "COMPLETED" {
				t.Fatalf("lifecycle %s %v", status, err)
			}
			chatIssueCount(t, f, "mission_comments", wantComments)
		})
	}
}

func TestAutonomousDelegationWithoutExecutionChatCannotBorrowBootIssue(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('nochat-source',?,?, 'nochat-run','run.started','orchestrator','scheduled', '{"trigger_type":"ASSIGNMENT","host_admitted":true}')`, f.workspace, f.agent)
	if _, err := runauthority.Resolve(t.Context(), f.h.db, "nochat-run", f.agent, f.workspace); err != nil {
		t.Fatalf("autonomous source fixture invalid: %v", err)
	}
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-NOCHAT-BOOT", "TODO")
	if err := ensureMissionChat(t.Context(), f.h.db, issue, f.workspace, f.agent, "Boot issue"); err != nil {
		t.Fatal(err)
	}
	h := NewAssignmentHandler(f.h.db, nil, nil, "host", newTestLogger())
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/", jsonBody(map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "actor_agent_id": f.agent, "source_run_id": "nochat-run", "chat_id": issue, "target_slug": "worker", "task": "scheduled task"})))
	if rr.Code != 403 || !strings.Contains(rr.Body.String(), "delegation_execution_chat_unavailable") {
		t.Fatalf("no-chat source borrowed boot context: %d %s", rr.Code, rr.Body.String())
	}
	var assignments int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE workspace_id=?`, f.workspace).Scan(&assignments); err != nil || assignments != 0 {
		t.Fatalf("unexpected assignment: count=%d err=%v", assignments, err)
	}
	chatIssueCount(t, f, "mission_comments", 0)
}
