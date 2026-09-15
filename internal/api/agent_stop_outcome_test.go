package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentStop_TerminalProjectionPreservesStopAndOtherRuns(t *testing.T) {
	for _, tc := range []struct {
		name, origin, status, want string
		other                      bool
	}{
		{"agent stop", "agent_stop", "CANCELLED", "STOPPED", false},
		{"chat cancel", "", "CANCELLED", "IDLE", false},
		{"other run remains", "agent_stop", "CANCELLED", "RUNNING", true},
		{"failure stays failure", "agent_stop", "FAILED", "ERROR", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, wsID, agentID := covIRunFixture(t)
			wireTestJournalForHandler(t, h.db, h)
			seedRunFixture(t, h.db, "stop-projection", agentID, wsID, "", "USER", "")
			if tc.other {
				seedRunFixture(t, h.db, "other-active-run", agentID, wsID, "", "USER", "")
			}
			req := httptest.NewRequest("PATCH", "/", jsonBody(map[string]any{"status": tc.status, "metadata": map[string]any{"stop_origin": tc.origin}}))
			req.SetPathValue("runId", "stop-projection")
			rr := httptest.NewRecorder()
			h.UpdateRun(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("%d: %s", rr.Code, rr.Body.String())
			}
			var got string
			if err := h.db.QueryRowContext(t.Context(), `SELECT status FROM agents WHERE id=?`, agentID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status=%s, want %s", got, tc.want)
			}
		})
	}
}
