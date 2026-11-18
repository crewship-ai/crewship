package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
)

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
