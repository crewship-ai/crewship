package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentStop_DaemonFailureDoesNotInventStopped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"daemon error", 503, `{"error":"cannot stop"}`},
		{"empty success", 200, `{}`},
		{"trailing garbage", 200, `{"agent_id":"agent-pxc","status":"stopped"}oops`},
		{"two values", 200, `{"agent_id":"agent-pxc","status":"stopped"}{}`},
		{"oversized confirmation", 200, `{"agent_id":"agent-pxc","status":"stopped"}` + strings.Repeat(" ", 4096)},
		{"another agent", 200, `{"agent_id":"someone-else","status":"stopped"}`},
		{"only requested", 200, `{"agent_id":"agent-pxc","status":"requested"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			h, user, ws, _, id := covProxyRig(t, sock)
			var before string
			if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.SetPathValue("agentId", id)
			req = withWorkspaceUser(req, user, ws, "OWNER")
			out := httptest.NewRecorder()
			h.AgentStop(out, req)
			if out.Code != http.StatusBadGateway {
				t.Errorf("unconfirmed stop returned %d, want 502", out.Code)
			}
			var after string
			if err := h.db.QueryRow(`SELECT status FROM agents WHERE id=?`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Errorf("unconfirmed stop changed %s to %s", before, after)
			}
		})
	}
}
