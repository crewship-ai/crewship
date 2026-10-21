package sidecar

import (
	"encoding/json"
	"net/http"
	"net/url"
)

func (s *Server) handleIssueShow(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.issueActor(w, r)
	if !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "/show")
	if !ok {
		return
	}
	var req struct {
		OperationKey string `json:"operation_key"`
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	runID, key, ok := s.issueOperationOrigin(w, r, req.OperationKey)
	if !ok {
		return
	}
	body, err := json.Marshal(map[string]string{"workspace_id": s.ipc.WorkspaceID, "agent_id": agentID, "author_run_id": runID, "operation_key": key})
	if err != nil {
		writeJSONResponse(w, 500, map[string]string{"error": "request_unavailable"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost, "/api/v1/internal/issues/"+url.PathEscape(ident)+"/show", "issue show", body)
}
