package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Both executions share the real sidecar; only the boot execution has a
// registry chat. The second must recover its execution chat from host journal.
func TestEscalationRealHostRecoversSecondRunChatAndRejectsMismatch(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('esc-human','esc@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-1','Escalations','escalations')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('esc-member','ws-1','esc-human','OWNER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-1','ws-1','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status) VALUES('agent-boot','ws-1','crew-1','Jamie','boot','LEAD','IDLE')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('chat-boot','ws-1','agent-boot','esc-human','private'),('chat-second','ws-1','agent-boot','esc-human','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	writer := journal.NewWriter(db, runTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-escalation-chain")})
	t.Cleanup(func() { writer.Close() })
	router, err := api.NewRouter(db, hostAuthorityJWTSecret, runTestLogger(), api.WithInternalToken(runTestMaster), api.WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/internal/escalations" {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, r)
			if rec.Code == 201 {
				var response map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Error(err)
				}
				if _, err := db.ExecContext(t.Context(), `UPDATE escalations SET status='RESOLVED',resolution='Reviewed',resolved_at=datetime('now') WHERE id=?`, response["escalation_id"]); err != nil {
					t.Error(err)
				}
			}
			for key, values := range rec.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer host.Close()
	post := func(path, token string, body any, proof string) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(t.Context(), "POST", host.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", token)
		req.Header.Set("X-Crewship-Run-Token", proof)
		res, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	sidecar, key := newRunIdentityServer(t)
	sidecar.ipc.BaseURL = host.URL
	sidecar.ipc.Token = internaltoken.DeriveCrewToken(runTestMaster, "ws-1", "crew-1")
	for _, tc := range []struct{ run, chat string }{{"run-boot", "chat-boot"}, {"second-esc-run", "chat-second"}} {
		receipt, err := chataudience.CaptureTrusted(t.Context(), db, tc.chat, "esc-human", nil)
		if err != nil || receipt == nil {
			t.Fatalf("receipt %v %v", receipt, err)
		}
		admission := post("/api/v1/internal/runs", runTestMaster, map[string]any{"id": tc.run, "workspace_id": "ws-1", "agent_id": "agent-boot", "chat_id": tc.chat, "human_authority": receipt, "trigger_message_id": "trigger-" + tc.run, "trigger_type": "USER"}, "")
		admission.Body.Close()
		if admission.StatusCode != 201 {
			t.Fatalf("admit %d", admission.StatusCode)
		}
		token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", tc.run)
		raw, _ := json.Marshal(map[string]any{"from": "boot", "reason": "Question " + tc.run})
		req := httptest.NewRequest("POST", "http://127.0.0.1:9119/escalate", bytes.NewReader(raw))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		sidecar.buildHandler(nil).ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("escalation run=%s %d %s", tc.run, rr.Code, rr.Body.String())
		}
		var chat string
		if err = db.QueryRow(`SELECT chat_id FROM escalations WHERE reason=?`, "Question "+tc.run).Scan(&chat); err != nil || chat != tc.chat {
			t.Fatalf("wrong execution chat=%s want%s err=%v", chat, tc.chat, err)
		}
	}
	proof := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "second-esc-run")
	for _, tc := range []struct{ name, run, chat, actor string }{{"wrong chat", "second-esc-run", "chat-boot", "agent-boot"}, {"wrong run", "run-boot", "chat-boot", "agent-boot"}, {"wrong actor", "second-esc-run", "chat-second", "foreign"}} {
		res := post("/api/v1/internal/escalations", sidecar.ipc.Token, map[string]any{"source_run_id": tc.run, "actor_agent_id": tc.actor, "chat_id": tc.chat, "workspace_id": "ws-1", "crew_id": "crew-1", "from_slug": "boot", "reason": tc.name}, proof)
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatalf("%s accepted %d", tc.name, res.StatusCode)
		}
	}
	if _, err = db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='esc-member'`); err != nil {
		t.Fatal(err)
	}
	res := post("/api/v1/internal/escalations", sidecar.ipc.Token, map[string]any{"source_run_id": "second-esc-run", "actor_agent_id": "agent-boot", "workspace_id": "ws-1", "crew_id": "crew-1", "reason": "Revoked"}, proof)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("revoked requester cannot ask for help: %d", res.StatusCode)
	}
	for _, tc := range []struct{ run, proof string }{{"second-esc-run", ""}, {"", proof}} {
		res := post("/api/v1/internal/escalations", sidecar.ipc.Token, map[string]any{"source_run_id": tc.run, "actor_agent_id": "agent-boot", "workspace_id": "ws-1", "crew_id": "crew-1", "reason": "Missing proof or source"}, tc.proof)
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatalf("unpaired proof/source accepted: %d", res.StatusCode)
		}
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-1", CrewID: "crew-1", AgentID: "agent-boot", Type: journal.EntryRunStarted, ActorType: journal.ActorOrchestrator, TraceID: "blocked-esc-run", Summary: "Blocked execution lifecycle", Payload: map[string]any{"chat_id": "chat-second", "requester_authority_missing": true}}); err != nil {
		t.Fatal(err)
	}
	blockedProof := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "blocked-esc-run")
	invoke := func(token, reason string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"from": "boot", "reason": reason})
		req := httptest.NewRequest("POST", "http://127.0.0.1:9119/escalate", bytes.NewReader(raw))
		req.RemoteAddr = "127.0.0.1:5000"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		sidecar.buildHandler(nil).ServeHTTP(rr, req)
		return rr
	}
	if rr := invoke(blockedProof, "Blocked sidecar execution"); rr.Code != 200 {
		t.Fatalf("blocked execution sidecar %d %s", rr.Code, rr.Body.String())
	}
	if rr := invoke(sidecar.ipc.AgentToken, "V1 downgrade"); rr.Code != 403 {
		t.Fatalf("v1 downgrade %d", rr.Code)
	}
	if rr := invoke("", "Missing v2 proof"); rr.Code != 403 {
		t.Fatalf("missing v2 proof %d", rr.Code)
	}

	res = post("/api/v1/internal/escalations", sidecar.ipc.Token, map[string]any{"source_run_id": "blocked-esc-run", "actor_agent_id": "agent-boot", "workspace_id": "ws-1", "crew_id": "crew-1", "reason": "Blocked run asks for help"}, blockedProof)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("blocked requester cannot escalate: %d", res.StatusCode)
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-1", CrewID: "crew-1", AgentID: "agent-boot", Type: journal.EntryRunCompleted, ActorType: journal.ActorOrchestrator, TraceID: "blocked-esc-run", Summary: "Blocked execution lifecycle", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	res = post("/api/v1/internal/escalations", sidecar.ipc.Token, map[string]any{"source_run_id": "blocked-esc-run", "actor_agent_id": "agent-boot", "workspace_id": "ws-1", "crew_id": "crew-1", "reason": "Ended run"}, blockedProof)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("ended run accepted: %d", res.StatusCode)
	}
	// Issue chats belong to the lead; the verified worker execution may still
	// ask its human from that same host-bound execution chat.
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status) VALUES('esc-worker','ws-1','crew-1','Worker','esc-worker','AGENT','IDLE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-1", CrewID: "crew-1", AgentID: "esc-worker", Type: journal.EntryRunStarted, ActorType: journal.ActorOrchestrator, TraceID: "worker-esc-run", Summary: "Worker on lead-owned issue chat", Payload: map[string]any{"chat_id": "chat-second", "requester_authority_missing": true}}); err != nil {
		t.Fatal(err)
	}
	workerProof := internaltoken.DeriveAgentRunToken(key, "ws-1", "esc-worker", "worker-esc-run")
	if rr := invoke(workerProof, "Worker needs help"); rr.Code != 200 {
		t.Fatalf("worker escalation %d %s", rr.Code, rr.Body.String())
	}
	var workerChat, workerAgent string
	if err := db.QueryRow(`SELECT chat_id,from_agent_id FROM escalations WHERE reason='Worker needs help'`).Scan(&workerChat, &workerAgent); err != nil || workerChat != "chat-second" || workerAgent != "esc-worker" {
		t.Fatalf("worker origin %s %s %v", workerChat, workerAgent, err)
	}

	// A genuinely legacy sidecar has no configured run key. Its own internal
	// service credential remains valid; it is never forwarded to the agent.
	sidecar.ipc.AgentRunKey = ""
	if rr := invoke(sidecar.ipc.AgentToken, "Legacy sidecar help"); rr.Code != 200 {
		t.Fatalf("legacy sidecar %d %s", rr.Code, rr.Body.String())
	}

}
