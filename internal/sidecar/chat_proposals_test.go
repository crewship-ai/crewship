package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestChatProposalSidecarVerifiedIdentity(t *testing.T) {
	server, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run")
	var received map[string]any
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/chat-proposals" {
			t.Error(r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proposal":{"status":"draft"}}`))
	}))
	defer host.Close()
	server.ipc.BaseURL = host.URL
	body := `{"title":"Plan","operation_key":"propose","items":[{"id":"one","title":"One","description":"","crew_id":"` + server.ipc.CrewID + `","project_id":null,"assignee_id":null,"priority":"none","selected":true}],"agent_id":"forged","run_id":"forged","chat_id":"forged"}`
	req := httptest.NewRequest("POST", "/chat/proposal/create", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	server.handleProposalCreate(rr, req)
	if rr.Code != 200 || calls != 1 {
		t.Fatalf("%d %s calls=%d", rr.Code, rr.Body, calls)
	}
	if received["agent_id"] != "agent-boot" || received["run_id"] != "verified-run" || received["workspace_id"] != "ws-1" || received["chat_id"] != nil {
		t.Fatalf("forged origin: %v", received)
	}
	req = httptest.NewRequest("POST", "/chat/proposal/create", strings.NewReader(body))
	rr = httptest.NewRecorder()
	server.handleProposalCreate(rr, req)
	if rr.Code != 403 || calls != 1 {
		t.Fatalf("legacy token: %d calls %d", rr.Code, calls)
	}
}
func TestChatProposalMCPUsesSameTransport(t *testing.T) {
	server, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got["operation_key"] != "stable-proposal" || got["run_id"] != "verified-run" {
			t.Errorf("origin: %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"proposal":{"status":"draft"}}`))
	}))
	defer host.Close()
	server.ipc.BaseURL = host.URL
	rr := issueMCPRequestForTest(t, server, "tools/call", map[string]any{"name": "propose_issues", "arguments": map[string]any{"request_key": "stable-proposal", "title": "Plan", "items": []any{map[string]any{"id": "one", "title": "One", "crew_id": server.ipc.CrewID, "priority": "none", "selected": true}}}}, token)
	if rr.Code != 200 || calls != 1 || strings.Contains(rr.Body.String(), `"isError":true`) {
		t.Fatalf("%d %s calls %d", rr.Code, rr.Body, calls)
	}
}
