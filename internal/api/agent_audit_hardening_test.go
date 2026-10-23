package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// The delete gate is re-decided on the writer transaction: a MANAGER who was
// demoted (or whose agent moved out of reach) after the first read must not
// delete on that stale answer. Not parallel: it sets an interleaving hook.
func TestAgentDeleteRechecksAuthorityInTransaction(t *testing.T) {
	tests := []struct {
		name       string
		concurrent string // SQL run between the first gate and the transaction
		want       int
	}{
		{"unchanged creator may delete", "", http.StatusOK},
		{"demoted to viewer meanwhile", `UPDATE workspace_members SET role='VIEWER' WHERE user_id='test-user-del-manager'`, http.StatusForbidden},
		{"removed from the workspace meanwhile", `DELETE FROM workspace_members WHERE user_id='test-user-del-manager'`, http.StatusForbidden},
		{"agent no longer theirs meanwhile", `UPDATE agents SET created_by_user_id=NULL WHERE id='del-agent'`, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			owner := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, owner)
			manager := seedMemberWithCapabilities(t, db, wsID, "MANAGER", `[]`, "del-manager")
			crewID := seedCrewRow(t, db, "del-crew", wsID, "Crew", "del-crew")
			seedAgentRow(t, db, "del-agent", wsID, crewID, "Agent", "del-agent", "AGENT")
			execOrFatal(t, db, `UPDATE agents SET created_by_user_id=? WHERE id='del-agent'`, manager)
			if tc.concurrent != "" {
				agentDeleteBeforeTx = func() { execOrFatal(t, db, tc.concurrent) }
				t.Cleanup(func() { agentDeleteBeforeTx = nil })
			}
			req := httptest.NewRequest("DELETE", "/api/v1/agents/del-agent", nil)
			req.SetPathValue("agentId", "del-agent")
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: manager}), wsID, "MANAGER"))
			rr := httptest.NewRecorder()
			NewAgentHandler(db, newTestLogger()).Delete(rr, req)
			var deleted int
			if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id='del-agent' AND deleted_at IS NOT NULL`).Scan(&deleted); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.want || (deleted == 1) != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d deleted=%d, want %d: %s", rr.Code, deleted, tc.want, rr.Body.String())
			}
		})
	}
}

// An agent-created agent's audit names what the caller's credentials prove:
// the bound workspace and crew, and the run and agent of a valid run token.
// A run token that does not verify is noted and never trusted; no token is
// recorded.
func TestInternalCreateAgentAuditsCaller(t *testing.T) {
	tests := []struct {
		name    string
		token   func(wsID, crewID string) string
		wantRun string
		invalid bool
	}{
		{"no run token", func(string, string) string { return "" }, "", false},
		{"valid run token", func(ws, crew string) string {
			return internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey("tok", ws, crew), ws, "lead-agent", "run-42")
		}, "run-42", false},
		{"forged run token", func(ws, crew string) string {
			return internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey("other-master", ws, crew), ws, "lead-agent", "run-42")
		}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, wsID, crewID, _, res := autonomyRig(t, "full")
			req := boundInternalReq(http.MethodPost, "/?workspace_id="+wsID, `{"crew_id":"`+crewID+`","name":"Forensic"}`, wsID, crewID)
			token := tc.token(wsID, crewID)
			if token != "" {
				req.Header.Set("X-Crewship-Run-Token", token)
			}
			rr := httptest.NewRecorder()
			gatedInternalHandler(t, db, res).CreateAgent(rr, req)
			if rr.Code >= 300 {
				t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
			}
			var raw string
			if err := db.QueryRow(`SELECT metadata FROM audit_logs WHERE action='agent.created'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatal(err)
			}
			if m["caller_token_crew_id"] != crewID || m["caller_token_workspace_id"] != wsID {
				t.Errorf("token binding not recorded: %s", raw)
			}
			if got, _ := m["caller_run_id"].(string); got != tc.wantRun {
				t.Errorf("caller_run_id = %q, want %q", got, tc.wantRun)
			}
			if tc.wantRun != "" && m["caller_agent_id"] != "lead-agent" {
				t.Errorf("caller_agent_id = %v", m["caller_agent_id"])
			}
			if (m["caller_run_token_invalid"] == true) != tc.invalid {
				t.Errorf("caller_run_token_invalid = %v, want %v", m["caller_run_token_invalid"], tc.invalid)
			}
			if token != "" && strings.Contains(raw, token) {
				t.Error("run token stored in the audit")
			}
		})
	}
}
