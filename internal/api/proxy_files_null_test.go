package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The agent file lists answer [] when the sidecar answers {"files": null}, as the crew list does:
// a client iterates the answer and must never see null.
func TestAgentFilesAnswersAnEmptyListForNullFiles(t *testing.T) {
	sock := covIPCJSON(t, map[string]any{
		"/crews/crew-pxc/files":           map[string]any{"files": nil},
		"/crews/crew-pxc/container-files": map[string]any{"files": nil},
	})
	h, userID, wsID, _, agentID := covProxyRig(t, sock)
	for name, handler := range map[string]http.HandlerFunc{"agent files": h.AgentFiles, "container files": h.AgentContainerFiles} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("agentId", agentID)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
			t.Errorf("%s: status = %d body=%q, want 200 []", name, rr.Code, rr.Body.String())
		}
	}
}
