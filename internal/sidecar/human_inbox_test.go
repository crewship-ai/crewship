package sidecar

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAgentInboxReadToolKeepsExactRunProofAndHostRefusal(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.Token = "synthetic-crew-ipc"
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-peer", "inbox-peer-run")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/internal/agent-inbox/read" || r.Method != "POST" || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" || r.Header.Get("X-Crewship-Run-Token") != token || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("lost scoped proof: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || body["run_id"] != "inbox-peer-run" || body["agent_id"] != "agent-peer" || body["workspace_id"] != "ws-1" {
			t.Errorf("unbound read source request: %+v", body)
		}
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"source authority unavailable"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	req := httptest.NewRequest("POST", "/mcp/routines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	code, body := s.readHumanInbox(req)
	if code != 403 || string(body) != `{"error":"source authority unavailable"}` {
		t.Fatalf("host refusal lost: %d %s", code, body)
	}
	req.Header.Set("Authorization", "Bearer forged")
	code, _ = s.readHumanInbox(req)
	if code != 403 || calls.Load() != 1 {
		t.Fatalf("forged actor reached host: %d calls=%d", code, calls.Load())
	}
}

func TestAgentInboxDiscoveryRequiresEnabledWorkspace(t *testing.T) {
	s, _ := newRunIdentityServer(t)
	for _, enabled := range []bool{false, true} {
		s.ipc.AgentInboxEnabled = enabled
		w := httptest.NewRecorder()
		s.respondRoutinesMCPToolsList(w, memoryMCPRequest{})
		var body struct {
			Result struct {
				Tools []struct{ Name string } `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range body.Result.Tools {
			found = found || tool.Name == "inbox_read"
		}
		if found != enabled {
			t.Fatalf("advertised inbox_read = %v with enabled = %v", found, enabled)
		}
	}
}
