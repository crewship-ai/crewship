package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDelegationForwardsOnlyTokenBoundSourceRun(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		handle           func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"assign", "/assign", `{"target":"peer","task":"work","source_run_id":"forged"}`, (*Server).handleAssign},
		{"query", "/query", `{"target":"peer","question":"work","source_run_id":"forged"}`, (*Server).handleQuery},
		{"mission", "/mission/create", `{"title":"Work","source_run_id":"forged"}`, (*Server).handleMissionCreate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received map[string]any
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"child"}`))
			}))
			defer upstream.Close()
			server, key := newRunIdentityServer(t)
			server.ipc.BaseURL = upstream.URL
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "source-run"))
			rr := httptest.NewRecorder()
			tc.handle(server, rr, req)
			if rr.Code != 200 {
				t.Fatalf("forward %d %s", rr.Code, rr.Body.String())
			}
			if received["source_run_id"] != "source-run" {
				t.Fatalf("source not token bound: %v", received)
			}
			if _, ok := received["chat_origin_id"]; ok {
				t.Fatal("agent supplied origin forwarded")
			}
			legacy := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			legacy.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentToken(runTestMaster, "ws-1", "agent-boot"))
			rejected := httptest.NewRecorder()
			tc.handle(server, rejected, legacy)
			if rejected.Code != 403 || calls != 1 {
				t.Fatalf("legacy source downgrade %d calls=%d %s", rejected.Code, calls, rejected.Body.String())
			}

		})
	}
}
