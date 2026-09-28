package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestInternalRunCrewScope(t *testing.T) {
	h, ids := seedScope(t)
	wireTestJournalForHandler(t, h.db, h)
	execOrFatal(t, h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('run-sibling',?,'Sibling','run-sibling')`, ids.wsA)
	execOrFatal(t, h.db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES('run-sibling-agent',?,'run-sibling','Sibling','run-sibling','IDLE')`, ids.wsA)
	call := func(handler http.HandlerFunc, run, crew string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := boundReq("POST", "/api/v1/internal/runs", raw, scopeMaster, ids.wsA)
		req.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(scopeMaster, ids.wsA, crew))
		req.SetPathValue("runId", run)
		rr := httptest.NewRecorder()
		h.requireInternal(handler).ServeHTTP(rr, req)
		return rr
	}
	for _, tc := range []struct {
		name, agent, workspace, chat string
		want                         int
	}{
		{"sibling", "run-sibling-agent", ids.wsA, "", 404},
		{"foreign", ids.agentB, ids.wsB, "", 403},
		{"foreign chat", ids.agentA, ids.wsA, ids.chatB, 404},
		{"own", ids.agentA, ids.wsA, ids.chatA, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := call(h.CreateRun, "", ids.crewA, map[string]any{"id": "scope-" + tc.name, "agent_id": tc.agent, "workspace_id": tc.workspace, "chat_id": tc.chat})
			if rr.Code != tc.want {
				t.Fatalf("got %d want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	if got := agentStatus(t, h.db, "run-sibling-agent"); got != "IDLE" {
		t.Errorf("sibling status changed: %s", got)
	}
	rr := call(h.CreateRun, "", "run-sibling", map[string]any{"id": "sibling-valid", "agent_id": "run-sibling-agent", "workspace_id": ids.wsA})
	if rr.Code != 201 {
		t.Fatalf("own sibling create: %d %s", rr.Code, rr.Body.String())
	}
	for _, status := range []string{"RUNNING", "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"} {
		t.Run("foreign update "+status, func(t *testing.T) {
			rr := call(h.UpdateRun, "sibling-valid", ids.crewA, map[string]any{"status": status})
			if rr.Code != 404 {
				t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id='sibling-valid' AND entry_type != 'run.started'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign terminal events: %d %v", n, err)
	}
	if got := agentStatus(t, h.db, "run-sibling-agent"); got != "RUNNING" {
		t.Errorf("foreign finalization changed status: %s", got)
	}
	rr = call(h.UpdateRun, "scope-own", ids.crewA, map[string]any{"status": "COMPLETED"})
	if rr.Code != 200 {
		t.Fatalf("own completion: %d %s", rr.Code, rr.Body.String())
	}
}
