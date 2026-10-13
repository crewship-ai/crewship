package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
)

// Seed through host CreateRun after writing the real JSONL triggering message.
// Tests must not grant authority by putting a user ID into an agent request.
func seedChatIssueRun(t *testing.T, h *InternalIssueHandler, workspace, agent, user, chat, run string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, chat, workspace, agent, user)
	store := conversation.NewStore(t.TempDir(), newTestLogger())
	t.Cleanup(store.Close)
	trigger := "trigger-" + run
	if err := store.Append(t.Context(), chat, conversation.Message{ID: trigger, Role: conversation.RoleUser, AuthorUserID: user, Content: "Create an issue"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), chat); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), h.db, chat, user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	host := NewInternalHandler(h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, h.db, host)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "workspace_id": workspace, "agent_id": agent, "chat_id": chat, "human_authority": receipt, "trigger_message_id": trigger, "trigger_type": "USER"}))
	rr := httptest.NewRecorder()
	host.CreateRun(rr, req)
	if rr.Code != 201 {
		t.Fatalf("start real chat run: %d %s", rr.Code, rr.Body.String())
	}
}

func seedAutonomousIssueRun(t *testing.T, h *InternalIssueHandler, workspace, agent, run string) {
	t.Helper()
	host := NewInternalHandler(h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, h.db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "workspace_id": workspace, "agent_id": agent, "trigger_type": "SCHEDULED"})))
	if rr.Code != 201 {
		t.Fatalf("start autonomous run: %d %s", rr.Code, rr.Body.String())
	}
}

type chatIssueFixture struct {
	h                                       *InternalIssueHandler
	workspace, crew, agent, user, chat, run string
}

func newChatIssueFixture(t *testing.T) chatIssueFixture {
	t.Helper()
	h, workspace, crew, agent, user := newInternalIssueHandler(t)
	f := chatIssueFixture{h, workspace, crew, agent, user, "operation-chat", "operation-run"}
	seedChatIssueRun(t, h, workspace, agent, user, f.chat, f.run)
	return f
}
func (f chatIssueFixture) createBody(key, title string) map[string]any {
	return map[string]any{"workspace_id": f.workspace, "crew_id": f.crew, "title": title, "author_agent_id": f.agent, "author_run_id": f.run, "author_chat_id": "forged-chat", "operation_key": key}
}
func chatIssueCall(t *testing.T, handler http.HandlerFunc, method, identifier string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/", jsonBody(body))
	req.SetPathValue("identifier", identifier)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}
func chatIssueJSON(t *testing.T, rr *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status=%d want=%d %s", rr.Code, want, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
func chatIssueCount(t *testing.T, f chatIssueFixture, table string, want int) {
	t.Helper()
	var n int
	switch table {
	case "missions", "agent_operations", "chat_entity_refs", "mission_comments":
	default:
		t.Fatal("unsupported count table")
	}
	if err := f.h.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != want {
		t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
	}
}

func TestChatIssueOperationsCreateReplayAndReadProjection(t *testing.T) {
	f := newChatIssueFixture(t)
	body := f.createBody("create-1", "Real issue")
	first := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 201)
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 200)
	if replay["replayed"] != true || replay["id"] != first["id"] || first["operation_id"] == nil || replay["operation_id"] != first["operation_id"] {
		t.Fatalf("replay mismatch: first=%v replay=%v", first, replay)
	}
	chatIssueCount(t, f, "missions", 1)
	chatIssueCount(t, f, "agent_operations", 1)
	chatIssueCount(t, f, "chat_entity_refs", 1)
	router := &Router{db: f.h.db, logger: newTestLogger()}
	req := httptest.NewRequest("GET", "/api/v1/chats/operation-chat/entity-refs", nil)
	req.SetPathValue("chatId", f.chat)
	req = req.WithContext(withWorkspace(withUser(context.Background(), &AuthUser{ID: f.user}), f.workspace, "OWNER"))
	rr := httptest.NewRecorder()
	router.chatEntityRefs(rr, req)
	if rr.Code != 200 {
		t.Fatalf("card read %d %s", rr.Code, rr.Body.String())
	}
	var page EntityRefPage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Refs) != 1 || page.Refs[0].Entity.ID != first["id"] || page.Refs[0].Anchor.RunID != f.run || page.Refs[0].Anchor.TriggerMessageID != "trigger-"+f.run {
		t.Fatalf("incorrect card: %+v", page)
	}
	if strings.Contains(rr.Body.String(), "forged-chat") || strings.Contains(rr.Body.String(), "human_authority") || strings.Contains(rr.Body.String(), "create-1") {
		t.Fatalf("private operation fields disclosed: %s", rr.Body.String())
	}
	body["title"] = "Different operation under same key"
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 409)
	chatIssueCount(t, f, "missions", 1)
}

func TestChatIssueOperationsRevocationAndEndedRunDenyReplay(t *testing.T) {
	for _, mutation := range []string{"revoke", "ended"} {
		t.Run(mutation, func(t *testing.T) {
			f := newChatIssueFixture(t)
			body := f.createBody("create", "Original")
			chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 201)
			if mutation == "revoke" {
				execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.workspace, f.user)
			} else {
				execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('ended',?,?,'run.completed','orchestrator','Ended',?)`, f.workspace, f.agent, f.run)
			}
			got := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 403)
			want := "requester_authority_stale"
			if mutation == "ended" {
				want = "run_identity_mismatch"
			}
			if got["error"] != want {
				t.Fatalf("error=%v want=%s", got, want)
			}
			chatIssueCount(t, f, "missions", 1)
			chatIssueCount(t, f, "agent_operations", 1)
			chatIssueCount(t, f, "chat_entity_refs", 1)
		})
	}
}

func TestChatIssueOperationsMemberCannotUpdate(t *testing.T) {
	h, workspace, crew, agent, user := newInternalIssueHandler(t)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, workspace, user)
	seedChatIssueRun(t, h, workspace, agent, user, "member-chat", "member-run")
	issue := seedIssue(t, h.db, workspace, crew, agent, "ENG-1", "BACKLOG")
	rr := chatIssueCall(t, h.UpdateStatus, "PATCH", "ENG-1", map[string]any{"workspace_id": workspace, "agent_id": agent, "author_run_id": "member-run", "operation_key": "update", "status": "TODO"})
	body := chatIssueJSON(t, rr, 403)
	if body["error"] != "requester_forbidden" {
		t.Fatalf("wrong error: %v", body)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, issue).Scan(&status); err != nil || status != "BACKLOG" {
		t.Fatalf("unauthorized mutation: %s %v", status, err)
	}
	f := chatIssueFixture{h: h}
	chatIssueCount(t, f, "agent_operations", 0)
	chatIssueCount(t, f, "chat_entity_refs", 0)
}

func TestChatIssueOperationsDeletedCreateCannotReplayResult(t *testing.T) {
	f := newChatIssueFixture(t)
	body := f.createBody("create", "Delete later")
	first := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 201)
	execOrFatal(t, f.h.db, `DELETE FROM missions WHERE id=?`, first["id"])
	rr := chatIssueCall(t, f.h.Create, "POST", "", body)
	if rr.Code != 403 && rr.Code != 404 {
		t.Fatalf("deleted create result replayed: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), first["id"].(string)) {
		t.Fatal("deleted entity ID disclosed")
	}
	chatIssueCount(t, f, "missions", 0)
}

func TestChatIssueOperationsRefFailureRollsBackMutation(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `CREATE TRIGGER reject_chat_ref BEFORE INSERT ON chat_entity_refs BEGIN SELECT RAISE(ABORT,'injected card failure'); END`)
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", f.createBody("create", "Rollback")), 500)
	chatIssueCount(t, f, "missions", 0)
	chatIssueCount(t, f, "agent_operations", 0)
	chatIssueCount(t, f, "chat_entity_refs", 0)
	execOrFatal(t, f.h.db, `DROP TRIGGER reject_chat_ref`)
	chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", f.createBody("create", "Rollback")), 201)
	chatIssueCount(t, f, "missions", 1)
}

func TestChatIssueOperationsCommentIsIdempotent(t *testing.T) {
	f := newChatIssueFixture(t)
	seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "comment", "body": "One comment"}
	first := chatIssueJSON(t, chatIssueCall(t, f.h.CreateComment, "POST", "ENG-1", body), 201)
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.CreateComment, "POST", "ENG-1", body), 200)
	if first["id"] != replay["id"] || first["operation_id"] == nil || replay["operation_id"] != first["operation_id"] || replay["replayed"] != true {
		t.Fatalf("comment replay: %v %v", first, replay)
	}
	chatIssueCount(t, f, "mission_comments", 1)
	chatIssueCount(t, f, "agent_operations", 1)
	chatIssueCount(t, f, "chat_entity_refs", 1)
}

func TestChatIssueOperationsStatusRetryAfterTransition(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "move", "status": "TODO"}
	first := chatIssueJSON(t, chatIssueCall(t, f.h.UpdateStatus, "PATCH", "ENG-1", body), 200)
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.UpdateStatus, "PATCH", "ENG-1", body), 200)
	if replay["replayed"] != true || first["operation_id"] == nil || replay["operation_id"] != first["operation_id"] {
		t.Fatalf("transition retry did not replay: first=%v replay=%v", first, replay)
	}
	var status string
	if err := f.h.db.QueryRow(`SELECT status FROM missions WHERE id=?`, issue).Scan(&status); err != nil || status != "TODO" {
		t.Fatalf("status=%s %v", status, err)
	}
	chatIssueCount(t, f, "agent_operations", 1)
	chatIssueCount(t, f, "chat_entity_refs", 1)
}

func TestChatIssueOperationsMissingKeyDeniedWithoutMutation(t *testing.T) {
	f := newChatIssueFixture(t)
	body := f.createBody("", "No key")
	result := chatIssueJSON(t, chatIssueCall(t, f.h.Create, "POST", "", body), 400)
	if result["error"] != "operation_key_required" {
		t.Fatalf("wrong error: %v", result)
	}
	chatIssueCount(t, f, "missions", 0)
	chatIssueCount(t, f, "agent_operations", 0)
	chatIssueCount(t, f, "chat_entity_refs", 0)
}

func TestChatIssueOperationsShowRecordsReferenceWithoutChangingIssue(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	var revision, aggregate int64
	if err := f.h.db.QueryRow(`SELECT revision,aggregate_revision FROM missions WHERE id=?`, issue).Scan(&revision, &aggregate); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "show"}
	first := chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", body), 201)
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.Show, "POST", "ENG-1", body), 200)
	if first["id"] != issue || first["operation_id"] == nil || replay["operation_id"] != first["operation_id"] || replay["replayed"] != true {
		t.Fatalf("show replay: first=%v replay=%v", first, replay)
	}
	var gotRevision, gotAggregate int64
	if err := f.h.db.QueryRow(`SELECT revision,aggregate_revision FROM missions WHERE id=?`, issue).Scan(&gotRevision, &gotAggregate); err != nil || gotRevision != revision || gotAggregate != aggregate {
		t.Fatalf("show changed issue: before=%d/%d after=%d/%d error=%v", revision, aggregate, gotRevision, gotAggregate, err)
	}
	chatIssueCount(t, f, "missions", 1)
	chatIssueCount(t, f, "mission_comments", 0)
	chatIssueCount(t, f, "agent_operations", 1)
	chatIssueCount(t, f, "chat_entity_refs", 1)
	var verb string
	if err := f.h.db.QueryRow(`SELECT verb FROM agent_operations`).Scan(&verb); err != nil || verb != "shown" {
		t.Fatalf("verb=%s %v", verb, err)
	}
}

func TestChatIssueOperationsClassifyRequiresRealRunAuthority(t *testing.T) {
	f := newChatIssueFixture(t)
	seedAutonomousIssueRun(t, f.h, f.workspace, f.agent, "scheduled-run")
	host := NewInternalHandler(f.h.db, "host", newTestLogger())
	wireTestJournalForHandler(t, f.h.db, host)
	// A container-scoped CreateRun may record its own telemetry, but cannot
	// promote its declaration of SCHEDULED into host autonomous authority.
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": "forged-autonomous", "workspace_id": f.workspace, "agent_id": f.agent, "trigger_type": "SCHEDULED", "metadata": map[string]any{"host_admitted": true}}))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, f.workspace))
	rr := httptest.NewRecorder()
	host.CreateRun(rr, req)
	if rr.Code != 201 {
		t.Fatalf("scoped telemetry setup: %d %s", rr.Code, rr.Body.String())
	}
	for _, tc := range []struct {
		run, agent, mode string
		want             int
	}{
		{f.run, f.agent, "human", 200},
		{"scheduled-run", f.agent, "autonomous", 200},
		{"forged-autonomous", f.agent, "", 403},
		{"missing", f.agent, "", 403},
		{f.run, "agent-worker", "", 403},
	} {
		t.Run(tc.run+"-"+tc.agent, func(t *testing.T) {
			got := chatIssueJSON(t, chatIssueCall(t, f.h.ClassifyRun, "POST", "", map[string]any{"workspace_id": f.workspace, "agent_id": tc.agent, "author_run_id": tc.run}), tc.want)
			if tc.want == 200 && (got["mode"] != tc.mode || len(got) != 1) {
				t.Fatalf("classification disclosed extra authority: %v", got)
			}
		})
	}
	chatIssueCount(t, f, "missions", 0)
	chatIssueCount(t, f, "agent_operations", 0)
	chatIssueCount(t, f, "chat_entity_refs", 0)
}

func TestChatIssueOperationsNoopReservesOperationKey(t *testing.T) {
	f := newChatIssueFixture(t)
	issue := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-1", "BACKLOG")
	var revision int64
	if err := f.h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, issue).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"workspace_id": f.workspace, "agent_id": f.agent, "author_run_id": f.run, "operation_key": "noop", "status": "BACKLOG"}
	first := chatIssueJSON(t, chatIssueCall(t, f.h.UpdateStatus, "PATCH", "ENG-1", body), 200)
	replay := chatIssueJSON(t, chatIssueCall(t, f.h.UpdateStatus, "PATCH", "ENG-1", body), 200)
	if first["operation_id"] == nil || replay["operation_id"] != first["operation_id"] || replay["replayed"] != true {
		t.Fatalf("noop not persisted: first=%v replay=%v", first, replay)
	}
	body["priority"] = "high"
	chatIssueJSON(t, chatIssueCall(t, f.h.UpdateStatus, "PATCH", "ENG-1", body), 409)
	var gotRevision int64
	if err := f.h.db.QueryRow(`SELECT revision FROM missions WHERE id=?`, issue).Scan(&gotRevision); err != nil || gotRevision != revision {
		t.Fatalf("noop/conflict changed issue: revision %d -> %d error=%v", revision, gotRevision, err)
	}
	chatIssueCount(t, f, "agent_operations", 1)
}
