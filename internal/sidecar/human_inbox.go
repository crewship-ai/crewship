package sidecar

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func (s *Server) humanInboxDiscovered(r *http.Request) bool {
	if s.ipc == nil || s.ipc.AgentRunKey == "" {
		return false
	}
	token := bearerToken(r)
	ws, _, run, ok := internaltoken.ValidateAgentRunToken(s.ipc.AgentRunKey, token)
	if !ok || ws != s.ipc.WorkspaceID {
		return false
	}
	// Match existing run authentication: replay locally after a verified token
	// arrives. Other token consumers keep their original first-use setup.
	s.runs.ensureDurable(s.ipc, s.logger)
	st, known := s.runs.lookup(run)
	return known && st.HumanInboxGranted && st.EndedAt.IsZero() && s.runs.current(r.Context(), run)
}

func (s *Server) handleInboxGrant(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil || !internaltoken.ValidateAgentInboxGrant(s.ipc.AgentRunKey, bearerToken(r), r.Header.Get(internaltoken.AgentInboxGrantHeader)) {
		writeRunEndError(w, http.StatusForbidden, "a host inbox grant is required")
		return
	}
	_, _, run, present, ok := s.actingRunIdentity(r)
	if !present || !ok || run == "" || !s.runs.grantHumanInbox(run) {
		writeRunEndError(w, http.StatusForbidden, "grant requires a live agent run")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readHumanInbox(r *http.Request) (int, []byte) {
	if s.ipc == nil {
		return 503, mustJSON(map[string]string{"error": "IPC not configured"})
	}
	agent, _, run, present, ok := s.actingRunIdentity(r)
	proof := s.liveRunToken(r)
	if !present || !ok || run == "" || proof == "" {
		return 403, mustJSON(map[string]string{"error": "current agent run token required"})
	}
	raw, _ := json.Marshal(map[string]string{"workspace_id": s.ipc.WorkspaceID, "agent_id": agent, "run_id": run})
	// Reuse the existing proof transport header without acquiring any routine
	// or issue mutation authority.
	ctx := context.WithValue(r.Context(), routineSourceTokenKey{}, proof)
	res, err := s.ipcRequestJSONWithRun(ctx, http.MethodPost, "/api/v1/internal/agent-inbox/read", raw, proof)
	if err != nil {
		return 502, mustJSON(map[string]string{"error": "inbox source unavailable"})
	}
	return res.status, res.body
}
