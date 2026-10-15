package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real authenticated public admission -> host run journal -> normal v2 sidecar
// HTTP tools. No chat is invented to make a web requester authoritative.
func TestWebIssueStartSidecarMutationsDelegationResultAndRevocation(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('web-human','web@example.test'),('web-starter','starter@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-1','Web','web')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('web-member','ws-1','web-human','OWNER'),('starter-member','ws-1','web-starter','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug,issue_prefix,max_concurrent_agents) VALUES('crew-1','ws-1','Web','web','ENG',1)`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,tool_profile,timeout_seconds,memory_enabled) VALUES('agent-boot','ws-1','crew-1','Lead','boot','LEAD','IDLE','CLAUDE_CODE','CODING',1800,0),('agent-peer','ws-1','crew-1','Peer','peer','AGENT','IDLE','CLAUDE_CODE','CODING',1800,0)`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, runTestLogger(), journal.WriterOptions{ChainKey: []byte("web-test-chain")})
	t.Cleanup(func() { _ = writer.Close() })
	router, err := api.NewRouter(db, hostAuthorityJWTSecret, runTestLogger(), api.WithInternalToken(runTestMaster), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(router)
	defer host.Close()
	validator, err := auth.NewJWTValidator(hostAuthorityJWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), "web-human", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	human, err := validator.IssueAccessToken("web-human", session.ID, "Web", "web@example.test")
	if err != nil {
		t.Fatal(err)
	}
	hostCall := func(path, token string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(t.Context(), "POST", host.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if token == runTestMaster {
			req.Header.Set("X-Internal-Token", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s status%d want%d %s", path, resp.StatusCode, want, data)
		}
		result := map[string]any{}
		_ = json.Unmarshal(data, &result)
		return result
	}
	created := hostCall("/api/v1/crews/crew-1/issues?workspace_id=ws-1", human, map[string]any{"title": "UI work", "assignee_type": "agent", "assignee_id": "agent-boot"}, 201)
	issue, ident := created["id"].(string), created["identifier"].(string)
	var initialOrigin string
	if err := db.QueryRow(`SELECT chat_origin_id FROM missions WHERE id=?`, issue).Scan(&initialOrigin); err != nil {
		t.Fatal(err)
	}
	starterSession, err := sessions.NewDBStore(db).Create(t.Context(), "web-starter", "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	starter, err := validator.IssueAccessToken("web-starter", starterSession.ID, "Starter", "starter@example.test")
	if err != nil {
		t.Fatal(err)
	}
	hostCall("/api/v1/crews/crew-1/issues/"+ident+"/start?workspace_id=ws-1", starter, map[string]any{}, 200)
	var origin string
	if err := db.QueryRow(`SELECT chat_origin_id FROM missions WHERE id=?`, issue).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin == "" || origin == initialOrigin {
		t.Fatal("fresh start did not capture its own admission")
	}
	hostCall("/api/v1/internal/runs", runTestMaster, map[string]any{"id": "web-issue-run", "workspace_id": "ws-1", "agent_id": "agent-boot", "chat_id": issue, "chat_origin_id": origin, "trigger_type": "ASSIGNMENT"}, 201)
	authority, err := runauthority.Resolve(t.Context(), db, "web-issue-run", "agent-boot", "ws-1")
	if err != nil || authority.ChatID != "" || authority.HumanAuthority == nil || authority.HumanAuthority.Kind != "web_issue" || authority.HumanAuthority.UserID != "web-starter" {
		t.Fatalf("web authority %+v %v", authority, err)
	}
	side, key := newRunIdentityServer(t)
	side.ipc.BaseURL = host.URL
	side.ipc.Token = internaltoken.DeriveCrewToken(runTestMaster, "ws-1", "crew-1")
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "web-issue-run")
	call := func(method, path, op string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "http://127.0.0.1:9119"+path, bytes.NewReader(raw))
		req.Host = "127.0.0.1:9119"
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if op != "" {
			req.Header.Set("Idempotency-Key", op)
		}
		rr := httptest.NewRecorder()
		side.buildHandler(nil).ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s status%d want%d %s", path, rr.Code, want, rr.Body.String())
		}
		result := map[string]any{}
		_ = json.Unmarshal(rr.Body.Bytes(), &result)
		return result
	}
	call("PATCH", "/issue/"+ident, "web-status", map[string]any{"status": "REVIEW"}, 200)
	call("POST", "/issue/"+ident+"/comment", "web-comment", map[string]any{"body": "Agent result"}, 201)
	delegated := call("POST", "/assign", "", map[string]any{"target": "peer", "task": "Review UI result"}, 201)
	assignment, ok := delegated["assignment_id"].(string)
	if !ok {
		t.Fatalf("assignment %+v", delegated)
	}
	var childOrigin string
	if err := db.QueryRow(`SELECT chat_origin_id FROM assignments WHERE id=?`, assignment).Scan(&childOrigin); err != nil || childOrigin != origin {
		t.Fatalf("child authority %s %v", childOrigin, err)
	}
	published, err := orchestrator.WriteAssignmentResultComment(t.Context(), db, assignment, "ws-1", issue, "agent-peer", "web-result", "Completed result", "2026-10-14T08:00:00Z")
	if err != nil || !published {
		t.Fatalf("result publication %v %v", published, err)
	}
	var cards int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs`).Scan(&cards); err != nil || cards != 0 {
		t.Fatalf("web work invented chat cards %d %v", cards, err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='starter-member'`); err != nil {
		t.Fatal(err)
	}
	denied := call("POST", "/issue/"+ident+"/comment", "web-revoked", map[string]any{"body": "Must not write"}, 403)
	if !strings.Contains(denied["error"].(string), "authority_stale") {
		t.Fatalf("wrong denial %+v", denied)
	}
	call("POST", "/assign", "", map[string]any{"target": "peer", "task": "Must not delegate"}, 403)
	if published, err := orchestrator.WriteAssignmentResultComment(t.Context(), db, assignment, "ws-1", issue, "agent-peer", "revoked-result", "Must not publish", "2026-10-14T08:01:00Z"); err == nil || published {
		t.Fatalf("revoked result %v %v", published, err)
	}
}
