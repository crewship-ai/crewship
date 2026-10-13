package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// This crosses both real routers over HTTP. Authority starts at the host's
// authenticated run-admission endpoint; no fake upstream accepts the provenance
// the sidecar happens to send. The resulting card is then read as the human.
func TestIssuesMCPRealHostCreatesReplaysAndRespectsRevocation(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('issue-human','synthetic-issue@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-1','Synthetic','synthetic')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('issue-member','ws-1','issue-human','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug,issue_prefix) VALUES('crew-1','ws-1','Synthetic crew','synthetic','ENG')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('agent-boot','ws-1','crew-1','Jamie','boot','LEAD','IDLE','CLAUDE_CODE','CODING',1800,0)`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('issue-chat','ws-1','agent-boot','issue-human','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, runTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-issue-journal-chain-key")})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, hostAuthorityJWTSecret, runTestLogger(), api.WithInternalToken(runTestMaster), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(router)
	defer host.Close()
	history := conversation.NewStore(t.TempDir(), runTestLogger())
	t.Cleanup(history.Close)
	if err := history.Append(t.Context(), "issue-chat", conversation.Message{ID: "issue-trigger", Role: conversation.RoleUser, AuthorUserID: "issue-human", Content: "Create a synthetic issue"}); err != nil {
		t.Fatal(err)
	}
	if err := history.SyncSession(t.Context(), "issue-chat"); err != nil {
		t.Fatal(err)
	}
	receipt, err := chataudience.CaptureTrusted(t.Context(), db, "issue-chat", "issue-human", nil)
	if err != nil || receipt == nil {
		t.Fatalf("receipt %+v %v", receipt, err)
	}
	admissionBody, _ := json.Marshal(map[string]any{"id": "issue-run", "workspace_id": "ws-1", "agent_id": "agent-boot", "chat_id": "issue-chat", "human_authority": receipt, "trigger_message_id": "issue-trigger", "trigger_type": "USER"})
	admission, err := http.NewRequestWithContext(t.Context(), "POST", host.URL+"/api/v1/internal/runs", bytes.NewReader(admissionBody))
	if err != nil {
		t.Fatal(err)
	}
	admission.Header.Set("Content-Type", "application/json")
	admission.Header.Set("X-Internal-Token", runTestMaster)
	admitted, err := host.Client().Do(admission)
	if err != nil {
		t.Fatal(err)
	}
	var admissionResult map[string]any
	_ = json.NewDecoder(admitted.Body).Decode(&admissionResult)
	admitted.Body.Close()
	if admitted.StatusCode != 201 {
		t.Fatalf("host admission %d %+v", admitted.StatusCode, admissionResult)
	}
	sidecar, key := newRunIdentityServer(t)
	sidecar.ipc.BaseURL = host.URL
	sidecar.ipc.Token = internaltoken.DeriveCrewToken(runTestMaster, "ws-1", "crew-1")
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "issue-run")
	call := func(name string, args map[string]any) memoryMCPToolCallResult {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		req := httptest.NewRequest("POST", "http://127.0.0.1:9119/mcp/issues", bytes.NewReader(body))
		req.Host = "127.0.0.1:9119"
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		sidecar.buildHandler(nil).ServeHTTP(rec, req)
		var response memoryMCPResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.Error != nil {
			t.Fatalf("MCP %d %s", rec.Code, rec.Body.String())
		}
		var result memoryMCPToolCallResult
		if err := json.Unmarshal(response.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	creation := map[string]any{"title": "Synthetic real issue", "request_key": "real-create"}
	first := call("create_issue", creation)
	if first.IsError {
		t.Fatalf("create %+v", first)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(first.Content[0].Text), &created); err != nil {
		t.Fatal(err)
	}
	replay := call("create_issue", creation)
	var replayed map[string]any
	_ = json.Unmarshal([]byte(replay.Content[0].Text), &replayed)
	if replay.IsError || replayed["replayed"] != true || replayed["id"] != created["id"] {
		t.Fatalf("replay %+v %+v", created, replayed)
	}
	identifier, ok := created["identifier"].(string)
	if !ok || identifier == "" {
		t.Fatalf("identifier %+v", created)
	}
	comment := call("comment_issue", map[string]any{"identifier": identifier, "body": "Synthetic comment", "request_key": "real-comment"})
	if comment.IsError {
		t.Fatalf("comment %+v", comment)
	}
	shown := call("show_issue", map[string]any{"identifier": identifier, "request_key": "real-show"})
	if shown.IsError {
		t.Fatalf("show %+v", shown)
	}
	validator, err := auth.NewJWTValidator(hostAuthorityJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), "issue-human", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	humanToken, err := validator.IssueAccessToken("issue-human", session.ID, "Synthetic", "synthetic@example.test")
	if err != nil {
		t.Fatal(err)
	}
	view, err := http.NewRequestWithContext(t.Context(), "GET", host.URL+"/api/v1/chats/issue-chat/entity-refs?workspace_id=ws-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	view.Header.Set("Authorization", "Bearer "+humanToken)
	visible, err := host.Client().Do(view)
	if err != nil {
		t.Fatal(err)
	}
	var page api.EntityRefPage
	err = json.NewDecoder(visible.Body).Decode(&page)
	visible.Body.Close()
	if err != nil || visible.StatusCode != 200 || len(page.Refs) != 3 {
		t.Fatalf("projection %d %+v %v", visible.StatusCode, page, err)
	}
	for _, ref := range page.Refs {
		if ref.Anchor.RunID != "issue-run" || ref.Anchor.TriggerMessageID != "issue-trigger" || ref.Entity.ID != created["id"] {
			t.Fatalf("wrong provenance %+v", ref)
		}
	}
	for table, want := range map[string]int{"missions": 1, "agent_operations": 3, "chat_entity_refs": 3, "mission_comments": 1} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s %d want %d %v", table, count, want, err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE workspace_members SET role='VIEWER' WHERE id='issue-member'`); err != nil {
		t.Fatal(err)
	}
	denied := call("comment_issue", map[string]any{"identifier": identifier, "body": "Denied comment", "request_key": "real-denied"})
	if !denied.IsError || !strings.Contains(denied.Content[0].Text, "requester_authority_stale") {
		t.Fatalf("revocation bypass %+v", denied)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM mission_comments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("denied mutation committed %d %v", count, err)
	}
}
