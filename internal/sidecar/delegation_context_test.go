package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestUnrosteredRunDelegationNeverBorrowsBootChat(t *testing.T) {
	for _, query := range []bool{false, true} {
		t.Run(map[bool]string{false: "assignment", true: "query"}[query], func(t *testing.T) {
			s, key := newRunIdentityServer(t)
			var body map[string]any
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":"accepted"}`))
			}))
			defer host.Close()
			s.ipc.BaseURL = host.URL
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "new-unrostered-agent", "new-run")
			raw := `{"target":"peer","task":"Work"}`
			if query {
				raw = `{"target":"peer","question":"Question"}`
			}
			req := httptest.NewRequest("POST", "/assign", strings.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			if query {
				s.handleQuery(rr, req)
			} else {
				s.handleAssign(rr, req)
			}
			if rr.Code != 200 {
				t.Fatalf("unrostered run %d %s", rr.Code, rr.Body.String())
			}
			if body["actor_agent_id"] != "new-unrostered-agent" || body["source_run_id"] != "new-run" || body["chat_id"] != "" {
				t.Fatalf("borrowed boot identity/chat: %+v", body)
			}
		})
	}
}
