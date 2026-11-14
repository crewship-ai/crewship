package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// #5336: save_page sends the same agent run token the artifact tools send, so
// the host can bind the save to its run. A per-agent (non-run) token is never
// forwarded: it is not a run token and the host would refuse it.
func TestSavePageForwardsTheRunToken(t *testing.T) {
	s, key := newRunIdentityServer(t)
	runToken := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	legacy := internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot")
	var got []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/pages/save" || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Errorf("lost crew transport: %s", r.URL.Path)
		}
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"slug":"ops"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	call := func(bearer string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_page","arguments":{"name":"Ops","panels":[{"id":"p1","schema":"status.v1","owner":"crew/c","producer":"agent/boot","sla_seconds":30,"span":4}]}}}`))
		r.Host = "127.0.0.1:9119"
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		if isError, text := documentCreateMCPResult(t, w); isError {
			t.Fatalf("save_page failed: %s", text)
		}
	}
	call(runToken)
	call(legacy)
	if len(got) != 2 || got[0] != "Bearer "+runToken || got[1] != "" {
		t.Fatalf("forwarded Authorization headers = %q, want run token then none", got)
	}
}
