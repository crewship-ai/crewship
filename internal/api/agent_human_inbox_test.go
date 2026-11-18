package api

import (
	"database/sql"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/work"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/groupchat"
)

const inboxTestMaster = "synthetic-inbox-master-for-tests"

func enableHumanInboxFixture(t *testing.T, db *sql.DB, workspace string) {
	t.Helper()
	execOrFatal(t, db, `INSERT OR IGNORE INTO feature_flag_overrides(id,flag_id,workspace_id,enabled)
		VALUES('inbox-enabled-'||?,'ffl_agent_human_inbox',?,1)`, workspace, workspace)
}

type humanInboxFixture struct {
	h                                                 *InternalHandler
	r                                                 *Router
	ws, crew, agent, user, chat, run, assignment, job string
}

func inboxChannelFixture(t *testing.T) humanInboxFixture {
	t.Helper()
	handler, ws, crew, agent, user := newInternalIssueHandler(t)
	db := handler.db
	enableHumanInboxFixture(t, db, ws)
	store := groupchat.New(db).WithInboxEnabled(func(w string) bool { return w == ws })
	channel, err := store.Create(t.Context(), ws, user, groupchat.CreateInput{Kind: "channel", Title: "Human inbox test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddAgent(t.Context(), ws, user, channel.ID, agent); err != nil {
		t.Fatal(err)
	}
	m, _, err := store.Send(t.Context(), ws, user, channel.ID, groupchat.SendInput{ClientID: "canonical-request", Content: "Only the actual source body", MentionedAgentIDs: []string{agent}})
	if err != nil {
		t.Fatal(err)
	}
	f := humanInboxFixture{h: NewInternalHandler(db, inboxTestMaster, newTestLogger()), r: &Router{db: db, logger: newTestLogger()}, ws: ws, crew: crew, agent: agent, user: user, chat: "inbox-execution-chat", run: "inbox-execution-run", assignment: "inbox-execution-assignment"}
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,title,origin) VALUES(?,?,?,?,?,'AGENT')`, f.chat, ws, agent, user, "Channel execution")
	execOrFatal(t, db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES(?,?,?,?,?,?,'QUEUED')`, f.assignment, ws, f.chat, agent, agent, "Canonical assignment context")
	if err := db.QueryRow(`SELECT id FROM workspace_conversation_agent_jobs WHERE message_id=?`, m.ID).Scan(&f.job); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE workspace_conversation_agent_jobs SET assignment_id=?,state='queued' WHERE id=?`, f.assignment, f.job)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := work.AcceptAssignmentTx(t.Context(), tx, f.assignment)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE work_items SET state='running',generation=1 WHERE id=?`, accepted.WorkID)
	execOrFatal(t, db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES(?,?,1,1,'test','2126-11-18T00:00:00Z','2026-11-18T00:00:00Z','2026-11-18T00:00:00Z')`, f.run, accepted.WorkID)
	execOrFatal(t, db, `UPDATE assignments SET status='RUNNING' WHERE id=?`, f.assignment)
	raw, _ := json.Marshal(map[string]any{"assignment_id": f.assignment, "chat_id": f.chat, "trigger_type": "ASSIGNMENT", "host_admitted": true, "requester_authority_missing": true})
	execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id) VALUES('inbox-start',?,?,datetime('now'),'run.started','info','orchestrator','Run started',?,'{}',?)`, ws, agent, string(raw), f.run)
	return f
}
func (f humanInboxFixture) call(t *testing.T, proof string, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"workspace_id": f.ws, "agent_id": f.agent, "run_id": f.run}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/agent-inbox/read", strings.NewReader(string(raw)))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(inboxTestMaster, f.ws, f.crew))
	req.Header.Set("X-Crewship-Run-Token", proof)
	rr := httptest.NewRecorder()
	f.h.requireInternal(withHostChatOrigin(http.HandlerFunc(f.h.AgentInboxRead))).ServeHTTP(rr, req)
	return rr
}
func (f humanInboxFixture) proof() string {
	return internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(inboxTestMaster, f.ws, f.crew), f.ws, f.agent, f.run)
}
func (f humanInboxFixture) count(t *testing.T, want int) {
	t.Helper()
	var n int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_inbox_receipts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("receipts=%d want=%d", n, want)
	}
}

func TestAgentInboxReadExplicitCurrentSourceAndIdempotence(t *testing.T) {
	f := inboxChannelFixture(t)
	// Running, projecting, or an operator's GET must never simulate agent read.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/human-inbox", nil), f.user, f.ws, "OWNER")
	req.SetPathValue("agentId", f.agent)
	rr := httptest.NewRecorder()
	f.r.agentHumanInbox(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Only the actual source body") || !strings.Contains(rr.Body.String(), `"read_at":null`) {
		t.Fatalf("operator projection=%d %s", rr.Code, rr.Body.String())
	}
	f.count(t, 0)
	first := f.call(t, f.proof(), nil)
	if first.Code != 200 {
		t.Fatalf("agent read=%d %s", first.Code, first.Body.String())
	}
	var a humanInboxEntry
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Kind != "channel_message" || a.ID != f.job || a.Content != "Only the actual source body" || a.ReadAt == nil {
		t.Fatalf("wrong source receipt: %+v", a)
	}
	again := f.call(t, f.proof(), nil)
	if again.Code != 200 {
		t.Fatalf("repeat=%d %s", again.Code, again.Body.String())
	}
	var b humanInboxEntry
	json.Unmarshal(again.Body.Bytes(), &b)
	if b.ReadAt == nil || *b.ReadAt != *a.ReadAt {
		t.Fatal("retry changed first read timestamp")
	}
	f.count(t, 1)
}

func TestAgentInboxReadSourceAuthorityGuards(t *testing.T) {
	cases := []struct {
		name, sql string
		badProof  bool
	}{
		{"forged run proof", "", true},
		{"stale generation", `UPDATE work_items SET generation=generation+1`, false},
		{"ended attempt", `UPDATE work_attempts SET ended_at=datetime('now')`, false},
		{"wrong work source", `UPDATE work_items SET domain_id='other-assignment'`, false},
		{"work moved to another workspace", `UPDATE work_items SET crew_id=''`, false},
		{"ended run", `INSERT INTO journal_entries(id,workspace_id,agent_id,ts,entry_type,severity,actor_type,summary,payload,refs,trace_id) SELECT 'ended-inbox-run',workspace_id,agent_id,datetime('now'),'run.completed','info','orchestrator','Ended','{}','{}',trace_id FROM journal_entries WHERE id='inbox-start'`, false},
		{"revoked requester", `UPDATE users SET authority_revision=authority_revision+1`, false},
		{"suspended requester", `UPDATE users SET suspended_at=datetime('now')`, false},
		{"membership epoch changed", `UPDATE workspace_members SET access_revision=access_revision+1`, false},
		{"sender no longer a member", `DELETE FROM workspace_members`, false},
		{"target removed from channel", `DELETE FROM workspace_conversation_agents`, false},
		{"canonical source deleted", `UPDATE workspace_conversations SET deleted_at=datetime('now')`, false},
		{"deleted target", `UPDATE agents SET deleted_at=datetime('now')`, false},
		{"foreign crew actor", `UPDATE agents SET crew_id=NULL`, false},
		{"wrong actual assignment", `UPDATE assignments SET assigned_to_id=(SELECT id FROM agents WHERE id<>assignments.assigned_to_id LIMIT 1)`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := inboxChannelFixture(t)
			if tc.sql != "" {
				execOrFatal(t, f.h.db, tc.sql)
			}
			proof := f.proof()
			if tc.badProof {
				proof = "forged"
			}
			rr := f.call(t, proof, nil)
			if rr.Code < 400 || rr.Code >= 500 {
				t.Fatalf("unsafe/unclear refusal=%d %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "Only the actual source body") {
				t.Fatal("refusal leaked canonical text")
			}
			f.count(t, 0)
		})
	}
}

func seedInboxPrivateRun(t *testing.T, db *sql.DB, ws, crew, agent, user, chat, run, content string) humanInboxFixture {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,?,?,'private')`, chat, ws, agent, user)
	store := conversation.NewStore(t.TempDir(), newTestLogger(), conversation.WithDB(db))
	t.Cleanup(store.Close)
	trigger := "trigger-" + run
	if err := store.Append(t.Context(), chat, conversation.Message{ID: trigger, AgentID: agent, Role: conversation.RoleUser, AuthorUserID: user, Content: content}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncSession(t.Context(), chat); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, chat, user, nil)
	if err != nil || receipt == nil {
		t.Fatalf("capture: %+v %v", receipt, err)
	}
	enableHumanInboxFixture(t, db, ws)
	host := NewInternalHandler(db, inboxTestMaster, newTestLogger())
	wireTestJournalForHandler(t, db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest("POST", "/api/v1/internal/runs", jsonBody(map[string]any{"id": run, "workspace_id": ws, "agent_id": agent, "chat_id": chat, "human_authority": receipt, "trigger_message_id": trigger, "trigger_type": "USER"})))
	if rr.Code != 201 {
		t.Fatalf("real host admission=%d %s", rr.Code, rr.Body.String())
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	accepted, err := work.NewStore(db).AcceptTx(t.Context(), tx, work.AcceptRequest{WorkspaceID: ws, Source: work.SourceChat, SourceRef: trigger, DomainKind: work.DomainHostChatTurn, DomainID: run, AgentID: agent, CrewID: crew, SessionID: chat, Class: work.ClassChat, AuthorizedByUserID: user, InputJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `UPDATE work_items SET state='running',generation=1 WHERE id=?`, accepted.WorkID)
	execOrFatal(t, db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES(?,?,1,1,'test','2126-11-18T00:00:00Z','2026-11-18T00:00:00Z','2026-11-18T00:00:00Z')`, run, accepted.WorkID)
	execOrFatal(t, db, `INSERT INTO agent_inbox_run_sources
		(run_id,workspace_id,agent_id,source_kind,source_id,source_sha256) VALUES(?,?,?,'chat_message',?,?)`,
		run, ws, agent, trigger, groupchat.InboxSourceDigest(content))
	return humanInboxFixture{h: host, r: &Router{db: db, logger: newTestLogger()}, ws: ws, crew: crew, agent: agent, user: user, chat: chat, run: run}
}

func TestAgentInboxReadPrivateChatDoesNotCrossSource(t *testing.T) {
	h, ws, crew, agent, user := newInternalIssueHandler(t)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id=?`, user)
	a := seedInboxPrivateRun(t, h.db, ws, crew, agent, user, "private-a", "private-run-a", "PRIVATE MESSAGE A")
	other := "private-human-b"
	execOrFatal(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Other person')`, other, other+"@test.invalid")
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('private-member-b',?,?,'MANAGER','trusted')`, ws, other)
	b := seedInboxPrivateRun(t, h.db, ws, crew, agent, other, "private-b", "private-run-b", "PRIVATE MESSAGE B")
	for _, source := range []humanInboxFixture{a, b} {
		rr := source.call(t, source.proof(), nil)
		if rr.Code != 200 {
			t.Fatalf("own source=%d %s", rr.Code, rr.Body.String())
		}
		expected := "PRIVATE MESSAGE A"
		unwanted := "PRIVATE MESSAGE B"
		if source.run == b.run {
			expected, unwanted = unwanted, expected
		}
		if !strings.Contains(rr.Body.String(), expected) || strings.Contains(rr.Body.String(), unwanted) {
			t.Fatalf("crossed private source: %s", rr.Body.String())
		}
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/human-inbox", nil), user, ws, "MANAGER")
	req.SetPathValue("agentId", agent)
	rr := httptest.NewRecorder()
	a.r.agentHumanInbox(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "PRIVATE MESSAGE A") || strings.Contains(rr.Body.String(), "PRIVATE MESSAGE B") {
		t.Fatalf("private human projection=%d %s", rr.Code, rr.Body.String())
	}
	if rr := a.call(t, b.proof(), nil); rr.Code != 403 {
		t.Fatalf("another source run proof=%d %s", rr.Code, rr.Body.String())
	}
	if rr := a.call(t, a.proof(), map[string]any{"source_id": "trigger-" + b.run}); rr.Code != 400 {
		t.Fatalf("opaque source selector=%d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id=?`, user)
	if rr := a.call(t, a.proof(), nil); rr.Code < 400 {
		t.Fatalf("revoked requester source read=%d %s", rr.Code, rr.Body.String())
	}
}

func TestAgentHumanInboxProjectionDoesNotAcknowledge(t *testing.T) {
	f := inboxChannelFixture(t)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+f.agent+"/human-inbox", nil), f.user, f.ws, "OWNER")
	req.SetPathValue("agentId", f.agent)
	rr := httptest.NewRecorder()
	f.r.agentHumanInbox(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Only the actual source body") {
		t.Fatalf("projection=%d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []humanInboxEntry `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].ReadAt != nil || body.Items[0].ID != f.job {
		t.Fatalf("wrong canonical unread projection %+v", body)
	}
	var n int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM agent_inbox_receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("operator GET acknowledged agent mail: %d %v", n, err)
	}
	for _, selector := range []map[string]any{{"source_id": "other-private-source"}, {"target_agent_id": "other"}, {"chat_id": "private"}} {
		bad := f.call(t, f.proof(), selector)
		if bad.Code != 400 {
			t.Fatalf("caller source selector admitted %d %s", bad.Code, bad.Body.String())
		}
	}
}

func TestAgentHumanInboxEditedMessageDoesNotReuseReceipt(t *testing.T) {
	f := inboxChannelFixture(t)
	if rr := f.call(t, f.proof(), nil); rr.Code != 200 {
		t.Fatalf("initial read=%d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, f.h.db, `UPDATE workspace_conversation_messages SET content='new unapproved content' WHERE id=(SELECT message_id FROM workspace_conversation_agent_jobs WHERE id=?)`, f.job)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/human-inbox", nil), f.user, f.ws, "OWNER")
	req.SetPathValue("agentId", f.agent)
	rr := httptest.NewRecorder()
	f.r.agentHumanInbox(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"read_at":null`) {
		t.Fatalf("old receipt applied to changed source=%d %s", rr.Code, rr.Body.String())
	}
	if rr := f.call(t, f.proof(), nil); rr.Code < 400 {
		t.Fatalf("old frozen source read changed bytes=%d %s", rr.Code, rr.Body.String())
	}
	f.count(t, 1)
}

func TestAgentInboxReadIssueBriefAndFrozenWorkVersion(t *testing.T) {
	f := newChatIssueFixture(t)
	enableHumanInboxFixture(t, f.h.db, f.workspace)
	input := humanInboxFixture{h: NewInternalHandler(f.h.db, inboxTestMaster, newTestLogger()), r: &Router{db: f.h.db, logger: newTestLogger()}, ws: f.workspace, crew: f.crew, agent: f.agent, user: f.user, chat: f.chat, run: f.run}
	id := seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-19", "IN_PROGRESS")
	execOrFatal(t, f.h.db, `UPDATE missions SET created_by_user_id=?,delegate_agent_id=? WHERE id=?`, f.user, f.agent, id)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,mission_id,assigned_by_id,assigned_to_id,task,status,created_by_user_id) VALUES('brief-read-assignment',?,?,?,?,?,'Frozen task instructions','QUEUED',?)`, f.workspace, f.chat, id, f.agent, f.agent, f.user)
	execOrFatal(t, f.h.db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,description,status,task_order,depends_on,assignment_id) VALUES('brief-read-task',?,?,'Task','','IN_PROGRESS',1,'[]','brief-read-assignment')`, id, f.agent)
	tx, err := f.h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := work.AcceptAssignmentTx(t.Context(), tx, "brief-read-assignment")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `UPDATE work_items SET state='running',generation=1 WHERE id=?`, receipt.WorkID)
	execOrFatal(t, f.h.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES(?,?,1,1,'test','2126-11-18T00:00:00Z','2026-11-18T00:00:00Z','2026-11-18T00:00:00Z')`, f.run, receipt.WorkID)
	execOrFatal(t, f.h.db, `UPDATE assignments SET status='RUNNING' WHERE id='brief-read-assignment'`)
	execOrFatal(t, f.h.db, `UPDATE journal_entries SET payload=json_set(payload,'$.assignment_id','brief-read-assignment') WHERE trace_id=? AND entry_type='run.started'`, f.run)
	if rr := input.call(t, input.proof(), nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Frozen task instructions") {
		t.Fatalf("frozen task read=%d %s", rr.Code, rr.Body.String())
	}
	req := withWorkspaceUser(httptest.NewRequest("GET", "/human-inbox", nil), f.user, f.workspace, "OWNER")
	req.SetPathValue("agentId", f.agent)
	rr := httptest.NewRecorder()
	input.r.agentHumanInbox(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"kind":"issue"`) {
		t.Fatalf("issue source=%d %s", rr.Code, rr.Body.String())
	}
	// Editing the canonical brief advances its version. The old runtime must
	// neither consume the new brief nor make its old receipt cover that version.
	execOrFatal(t, f.h.db, `UPDATE missions SET description='New brief not accepted by the old run' WHERE id=?`, id)
	if rr := input.call(t, input.proof(), nil); rr.Code < 400 {
		t.Fatalf("stale brief read=%d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	input.r.agentHumanInbox(rr, req)
	var body struct {
		Items []humanInboxEntry `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, e := range body.Items {
		if e.ID == id && e.ReadAt != nil {
			t.Fatalf("edited brief inherited read receipt: %+v", e)
		}
	}
}

func TestAgentHumanInboxPrivateCommitmentStaysOutOfJournal(t *testing.T) {
	h, ws, crew, agent, user := newInternalIssueHandler(t)
	f := seedInboxPrivateRun(t, h.db, ws, crew, agent, user, "private-commitment", "private-commitment-run", "A short private message with a distinctive hash")
	expected := groupchat.InboxSourceDigest("A short private message with a distinctive hash")
	var stored string
	if err := h.db.QueryRow(`SELECT source_sha256 FROM agent_inbox_run_sources WHERE run_id=?`, f.run).Scan(&stored); err != nil || stored != expected {
		t.Fatalf("private host source commitment=%q %v", stored, err)
	}
	var payload string
	if err := h.db.QueryRow(`SELECT payload FROM journal_entries WHERE trace_id=? AND entry_type='run.started'`, f.run).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, expected) || strings.Contains(payload, "trigger_message_sha256") {
		t.Fatal("private source digest persisted onto public journal transport")
	}
}

func TestAgentInboxReadPublicIssueOrigin(t *testing.T) {
	h, user, ws, crew, _, agent := newTestIssueHandler(t)
	create := withWorkspaceUser(httptest.NewRequest("POST", "/issues", strings.NewReader(`{"title":"Public inbox task","description":"Public task instructions","delegate_agent_id":"`+agent+`"}`)), user, ws, "OWNER")
	create.SetPathValue("crewId", crew)
	create.Header.Set("Idempotency-Key", "public-task-create")
	created := httptest.NewRecorder()
	h.Create(created, create)
	if created.Code != 201 {
		t.Fatalf("public create=%d %s", created.Code, created.Body.String())
	}
	var issue issueResponse
	if err := json.Unmarshal(created.Body.Bytes(), &issue); err != nil {
		t.Fatal(err)
	}
	start := withWorkspaceUser(httptest.NewRequest("POST", "/issues/start", nil), user, ws, "OWNER")
	start.SetPathValue("crewId", crew)
	start.SetPathValue("identifier", *issue.Identifier)
	started := httptest.NewRecorder()
	h.Start(started, start)
	if started.Code != 200 {
		t.Fatalf("public start=%d %s", started.Code, started.Body.String())
	}
	var origin, task string
	if err := h.db.QueryRow(`SELECT chat_origin_id FROM missions WHERE id=?`, issue.ID).Scan(&origin); err != nil || origin == "" {
		t.Fatalf("public origin=%q %v", origin, err)
	}
	if err := h.db.QueryRow(`SELECT id FROM mission_tasks WHERE mission_id=? LIMIT 1`, issue.ID).Scan(&task); err != nil {
		t.Fatal(err)
	}
	// Model execution is replaced only by the exact canonical assignment/work
	// admission. Its public web_issue origin is the one captured by real Start.
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,origin) VALUES('public-inbox-chat',?,?,?,'AGENT')`, ws, agent, user)
	execOrFatal(t, h.db, `INSERT INTO assignments(id,workspace_id,chat_id,mission_id,assigned_by_id,assigned_to_id,task,status,created_by_user_id,chat_origin_id) VALUES('public-inbox-assignment',?,'public-inbox-chat',?,?,?,'Public task instructions','QUEUED',?,?)`, ws, issue.ID, agent, agent, user, origin)
	execOrFatal(t, h.db, `UPDATE mission_tasks SET assignment_id='public-inbox-assignment' WHERE id=?`, task)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := work.AcceptAssignmentTx(t.Context(), tx, "public-inbox-assignment")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE work_items SET state='running',generation=1 WHERE id=?`, accepted.WorkID)
	execOrFatal(t, h.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at) VALUES('public-inbox-run',?,1,1,'test','2126-11-18T00:00:00Z','2026-11-18T00:00:00Z','2026-11-18T00:00:00Z')`, accepted.WorkID)
	execOrFatal(t, h.db, `UPDATE assignments SET status='RUNNING' WHERE id='public-inbox-assignment'`)
	enableHumanInboxFixture(t, h.db, ws)
	host := NewInternalHandler(h.db, inboxTestMaster, newTestLogger())
	wireTestJournalForHandler(t, h.db, host)
	rr := httptest.NewRecorder()
	host.CreateRun(rr, httptest.NewRequest("POST", "/internal/runs", jsonBody(map[string]any{"id": "public-inbox-run", "workspace_id": ws, "agent_id": agent, "chat_id": "public-inbox-chat", "chat_origin_id": origin, "trigger_type": "ASSIGNMENT"})))
	if rr.Code != 201 {
		t.Fatalf("public run admission=%d %s", rr.Code, rr.Body.String())
	}
	// Native assignments_run.go emits assignment_id on the run.started
	// payload itself. CreateRun above validates the real public origin; add
	// the native emitter's exact linkage without pretending its IPC body
	// accepts a top-level assignment_id argument.
	execOrFatal(t, h.db, `UPDATE journal_entries SET payload=json_set(payload,'$.assignment_id','public-inbox-assignment') WHERE trace_id='public-inbox-run' AND entry_type='run.started'`)
	f := humanInboxFixture{h: host, r: &Router{db: h.db}, ws: ws, crew: crew, agent: agent, user: user, chat: "public-inbox-chat", run: "public-inbox-run"}
	if rr = f.call(t, f.proof(), nil); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Public task instructions") {
		t.Fatalf("public task ack=%d %s", rr.Code, rr.Body.String())
	}
	// Public execution uses its typed workspace web-execution epoch, not
	// the independent chat/account receipt epoch.
	execOrFatal(t, h.db, `UPDATE workspace_members SET web_execution_revision=web_execution_revision+1 WHERE workspace_id=? AND user_id=?`, ws, user)
	if rr = f.call(t, f.proof(), nil); rr.Code < 400 {
		t.Fatalf("public requester revoked=%d %s", rr.Code, rr.Body.String())
	}
}

func TestAgentHumanInboxFlagUsesWorkspaceOverride(t *testing.T) {
	f := inboxChannelFixture(t)
	req := func() *http.Request {
		r := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/human-inbox", nil), f.user, f.ws, "MANAGER")
		r.SetPathValue("agentId", f.agent)
		return r
	}
	execOrFatal(t, f.h.db, "DELETE FROM feature_flag_overrides WHERE workspace_id=? AND flag_id='ffl_agent_human_inbox'", f.ws)
	rec := httptest.NewRecorder()
	f.r.agentHumanInbox(rec, req())
	if rec.Code != http.StatusNotFound || f.call(t, f.proof(), nil).Code != http.StatusNotFound {
		t.Fatalf("flag default exposed inbox: %d %s", rec.Code, rec.Body.String())
	}
	enableHumanInboxFixture(t, f.h.db, f.ws)
	rec = httptest.NewRecorder()
	f.r.agentHumanInbox(rec, req())
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace override did not enable inbox: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentInboxReadLegacyChannelJobUsesCurrentAuthority(t *testing.T) {
	f := inboxChannelFixture(t)
	execOrFatal(t, f.h.db, "UPDATE workspace_conversation_agent_jobs SET read_authority_json=NULL,read_source_sha256=NULL WHERE id=?", f.job)
	if rr := f.call(t, f.proof(), nil); rr.Code != http.StatusOK {
		t.Fatalf("legacy live source rejected: %d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, f.h.db, "UPDATE users SET suspended_at='2026-11-19' WHERE id=?", f.user)
	if rr := f.call(t, f.proof(), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("revoked legacy source exposed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAgentInboxUnavailableWorkspaceDatabaseReturnsServerError(t *testing.T) {
	f := inboxChannelFixture(t)
	m := NewAuthMiddleware(nil, nil, f.h.db, newTestLogger())
	handler := m.RequireWorkspace(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("workspace authority failure reached the inbox handler")
	}))
	if err := f.h.db.Close(); err != nil {
		t.Fatal(err)
	}
	url := "/api/v1/agents/" + f.agent + "/human-inbox?workspace_id=" + f.ws
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, url, nil), f.user, f.ws, "OWNER")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code < 500 {
		t.Fatalf("unavailable membership database: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAgentInboxRechecksViewerAfterWorkspaceAdmission(t *testing.T) {
	f := inboxChannelFixture(t)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/human-inbox", nil), f.user, f.ws, "OWNER")
	req.SetPathValue("agentId", f.agent)
	// The real request can already have passed the mode guard when a member
	// becomes restricted. Database authority must govern the response.
	execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, f.ws, f.user)
	execOrFatal(t, f.h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE workspace_id=? AND user_id=?`, f.ws, f.user)
	rec := httptest.NewRecorder()
	(&Router{db: f.h.db, logger: newTestLogger()}).agentHumanInbox(rec, req)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Only the actual source body") {
		t.Fatalf("revoked viewer got inbox response %d %s", rec.Code, rec.Body.String())
	}
}
