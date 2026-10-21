package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestRoutineOriginMCPUsesTokenRunAndRefusesLegacy(t *testing.T) {
	for _, tool := range []string{"run_routine", "save_routine", "save_routine_draft"} {
		t.Run(tool, func(t *testing.T) {
			calls := 0
			s, key := newRunIdentityServer(t)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if strings.HasSuffix(r.URL.Path, "/test_run") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"passed","save_token":"test-proof"}`))
					return
				}
				ws, agent, run, valid := internaltoken.ValidateAgentRunToken(key, r.Header.Get("X-Crewship-Run-Token"))
				if !valid || ws != "ws-1" || agent != "agent-boot" || run != body["source_run_id"] {
					t.Errorf("outbound run credential is missing or disagrees with verified body: ws=%s agent=%s run=%s valid=%v", ws, agent, run, valid)
				}
				if tool == "run_routine" && (body["invoking_agent_id"] != agent || body["workspace_id"] != ws || r.URL.Path != "/api/v1/internal/pipelines/run") {
					t.Errorf("run transport identity mismatch: path=%s body=%+v", r.URL.Path, body)
				}
				if body["source_run_id"] != "verified-run" {
					t.Errorf("unbound source: %+v", body)
				}
				if _, ok := body["chat_origin_id"]; ok {
					t.Error("tool supplied origin")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"result"}`))
			}))
			defer host.Close()
			s.ipc.BaseURL = host.URL
			args := `{"slug":"routine","source_run_id":"forged","invoking_agent_id":"forged-agent","workspace_id":"forged-workspace","chat_origin_id":"forged"}`
			if tool == "save_routine" {
				args = `{"name":"Routine","definition":{"name":"Routine","steps":[]},"source_run_id":"forged","chat_origin_id":"forged"}`
			}
			if tool == "save_routine_draft" {
				args = `{"slug":"routine","draft":{"slug":"routine","revision":0,"document":{}},"source_run_id":"forged","chat_origin_id":"forged"}`
			}
			payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + `}}`
			req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "verified-run"))
			rr := httptest.NewRecorder()
			s.handleRoutinesMCP(rr, req)
			if rr.Code != 200 || calls == 0 {
				t.Fatalf("tool failed: %d %s calls=%d", rr.Code, rr.Body.String(), calls)
			}
			before := calls
			req = httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(payload))
			req.Header.Set("Authorization", "Bearer legacy-token")
			rr = httptest.NewRecorder()
			s.handleRoutinesMCP(rr, req)
			if calls != before {
				t.Fatal("legacy token invoked host")
			}
		})
	}
}
