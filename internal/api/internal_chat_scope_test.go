package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestInternalChatCrewBoundary(t *testing.T) {
	h, ids := seedScope(t)
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('sibling',?,'Sibling','sibling')`, ids.wsA)
	execOrFatal(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('sibling-agent',?,'sibling','Sibling','sibling')`, ids.wsA)
	execOrFatal(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,title) VALUES('sibling-chat',?,'sibling-agent','')`, ids.wsA)
	call := func(handler http.HandlerFunc, method, id string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := boundReq(method, "/api/v1/internal/chats", raw, scopeMaster, ids.wsA)
		req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(scopeMaster, ids.wsA, ids.crewA))
		req.SetPathValue("chatId", id)
		req.SetPathValue("agentId", id)
		rr := httptest.NewRecorder()
		h.requireInternal(handler).ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		name, agent, ws, chat string
		want                  int
	}{
		{"own", ids.agentA, ids.wsA, "new-own", 201},
		{"retry", ids.agentA, ids.wsA, "new-own", 200},
		{"sibling", "sibling-agent", ids.wsA, "new-sibling", 404},
		{"foreign agent forged workspace", ids.agentB, ids.wsA, "new-foreign", 404},
		{"foreign workspace", ids.agentB, ids.wsB, "new-foreign-ws", 403},
		{"foreign existing id", ids.agentA, ids.wsA, ids.chatB, 409},
		{"sibling existing id", ids.agentA, ids.wsA, "sibling-chat", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := call(h.CreateChat, "POST", "", map[string]any{"chat_id": tc.chat, "agent_id": tc.agent, "workspace_id": tc.ws})
			if rr.Code != tc.want {
				t.Fatalf("got %d want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	for _, id := range []string{"sibling-chat", ids.chatB, "missing"} {
		for _, tc := range []struct {
			name    string
			handler http.HandlerFunc
			body    map[string]any
		}{
			{"resolve", h.ResolveChat, nil},
			{"count", h.IncrementMessageCount, map[string]any{"delta": 1}},
			{"title", h.UpdateChatTitle, map[string]any{"title": "forged"}},
		} {
			t.Run(tc.name+"/"+id, func(t *testing.T) {
				if rr := call(tc.handler, "PATCH", id, tc.body); rr.Code != 404 {
					t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
				}
			})
		}
	}
	if rr := call(h.ResolveAgent, "GET", "sibling-agent", nil); rr.Code != 404 {
		t.Fatalf("sibling config leaked: %d", rr.Code)
	}
	for _, tc := range []struct {
		handler http.HandlerFunc
		body    map[string]any
	}{
		{h.IncrementMessageCount, map[string]any{"delta": 1}},
		{h.UpdateChatTitle, map[string]any{"title": "own"}},
	} {
		if rr := call(tc.handler, "PATCH", ids.chatA, tc.body); rr.Code != 200 {
			t.Fatalf("own mutation: %d %s", rr.Code, rr.Body.String())
		}
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM chats WHERE id LIKE 'new-%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unauthorized rows inserted: count=%d err=%v", count, err)
	}
	var title string
	if err := h.db.QueryRow(`SELECT title FROM chats WHERE id='sibling-chat'`).Scan(&title); err != nil || title != "" {
		t.Fatalf("sibling mutated: %q %v", title, err)
	}
}

// Workspace and host callers intentionally retain access across crews. A
// crew-bound context remains authoritative even without middleware URL injection.
func TestInternalAgentResolveScopeCompatibility(t *testing.T) {
	h, ids := seedScope(t)
	for _, tc := range []struct{ name, token string }{
		{"workspace", internaltoken.DeriveWorkspaceToken(scopeMaster, ids.wsA)},
		{"host", scopeMaster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := boundReq("GET", "/api/v1/internal/agents/resolve", nil, scopeMaster, ids.wsA)
			req.Header.Set("X-Internal-Token", tc.token)
			req.SetPathValue("agentId", ids.agentA)
			rr := httptest.NewRecorder()
			h.requireInternal(http.HandlerFunc(h.ResolveAgent)).ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
	req := httptest.NewRequest("GET", "/api/v1/internal/agents/resolve?workspace_id="+ids.wsB, nil)
	req = req.WithContext(crewBoundCtx1186(ids.wsA, ids.crewA))
	req.SetPathValue("agentId", ids.agentB)
	rr := httptest.NewRecorder()
	h.ResolveAgent(rr, req)
	if rr.Code != 404 {
		t.Fatalf("query overrode token context: %d", rr.Code)
	}
}

func TestInternalChatRetryPreservesRoutineIdentity(t *testing.T) {
	h, ids := seedScope(t)
	execOrFatal(t, h.db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('retry-pipeline',?,'retry','Retry','{}','hash')`, ids.wsA)
	execOrFatal(t, h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES('retry-run',?,'retry-pipeline','retry','running','2026-09-27')`, ids.wsA)
	for _, tc := range []struct {
		name, step string
		want       int
	}{
		{"create", "first", 201}, {"retry", "first", 200}, {"different step", "second", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"chat_id": "retry-chat", "agent_id": ids.agentA, "workspace_id": ids.wsA, "origin": "ROUTINE", "pipeline_run_id": "retry-run", "pipeline_step_id": tc.step})
			req := boundReq("POST", "/api/v1/internal/chats", raw, scopeMaster, ids.wsA)
			rr := httptest.NewRecorder()
			h.requireInternal(http.HandlerFunc(h.CreateChat)).ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("got %d want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}
