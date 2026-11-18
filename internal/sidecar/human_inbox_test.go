package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
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
	s, key := newRunIdentityServer(t)
	off := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-peer", "off-run")
	on := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-peer", "on-run")
	grant := internaltoken.DeriveAgentInboxGrant(key, on)
	register := func(token, proof string) int {
		req := httptest.NewRequest(http.MethodPost, "/agent/run/inbox-grant", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(internaltoken.AgentInboxGrantHeader, proof)
		rec := httptest.NewRecorder()
		s.handleInboxGrant(rec, req)
		return rec.Code
	}
	if status := register(off, grant); status != http.StatusForbidden {
		t.Fatalf("grant crossed runs: %d", status)
	}
	if status := register(on, grant); status != http.StatusNoContent {
		t.Fatalf("registration from this run failed: %d", status)
	}
	for _, tc := range []struct {
		token   string
		enabled bool
	}{
		{off, false},
		{on, true},
		{off, false},
		{on + "0", false},
		{"", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/mcp/routines", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.respondRoutinesMCPToolsList(w, r, memoryMCPRequest{})
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
		if found != tc.enabled {
			t.Fatalf("advertised inbox_read = %v with enabled = %v", found, tc.enabled)
		}
	}
	s.runs.end("on-run")
	r := httptest.NewRequest(http.MethodPost, "/mcp/routines", nil)
	r.Header.Set("Authorization", "Bearer "+on)
	if s.humanInboxDiscovered(r) {
		t.Fatal("revoked run retained inbox discovery")
	}

}

func TestAgentInboxGrantSurvivesSidecarRestartAndRevocation(t *testing.T) {
	t.Setenv(runStateDirEnv, t.TempDir())
	s, key := newRunIdentityServer(t)
	s.ipc.ContainerID = "inbox-container"
	bearer := internaltoken.DeriveAgentRunToken(key, s.ipc.WorkspaceID, "agent-peer", "grant-run")
	call := httptest.NewRequest(http.MethodPost, "/agent/run/inbox-grant", nil)
	call.Header.Set("Authorization", "Bearer "+bearer)
	call.Header.Set(internaltoken.AgentInboxGrantHeader, internaltoken.DeriveAgentInboxGrant(key, bearer))
	reg := httptest.NewRecorder()
	s.handleInboxGrant(reg, call)
	if reg.Code != http.StatusNoContent {
		t.Fatalf("grant registration returned %d", reg.Code)
	}
	recovered := &Server{ipc: s.ipc, runs: newRunRegistry()}
	if !recovered.humanInboxDiscovered(call) {
		t.Fatal("live run lost its discovery grant after replay")
	}
	recovered.runs.end("grant-run")
	afterEnd := &Server{ipc: s.ipc, runs: newRunRegistry()}
	if afterEnd.humanInboxDiscovered(call) {
		t.Fatal("revoked run retained discovery after replay")
	}
}
