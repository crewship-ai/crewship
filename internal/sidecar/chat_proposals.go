package sidecar

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// Only intent is accepted from the model; IPC identity and token-bound run are
// injected by this transport. Proposal creation never grants issue-write rights.
func (s *Server) handleProposalCreate(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, 503, map[string]string{"error": "IPC not configured"})
		return
	}
	var body struct {
		chatentities.ProposalCreate
		OperationKey string `json:"operation_key"`
	}
	if !decodeCappedJSON(w, r, &body) {
		return
	}
	agent, _, run, present, matched := s.actingRunIdentity(r)
	if !present || !matched || run == "" {
		writeJSONResponse(w, 403, map[string]string{"error": "verified run required"})
		return
	}
	_, key, ok := s.issueOperationOrigin(w, r, body.OperationKey)
	if !ok {
		return
	}
	if strings.TrimSpace(key) == "" {
		writeJSONResponse(w, 400, map[string]string{"error": "operation_key_required"})
		return
	}
	// Agent board reads are crew-scoped. Model-chosen foreign crew targets must
	// not become an existence oracle, even though human writes are broader.
	for _, item := range body.Items {
		if item.CrewID != s.ipc.CrewID {
			writeJSONResponse(w, 404, map[string]string{"error": "Card not found"})
			return
		}
	}
	raw, err := json.Marshal(map[string]any{"title": body.Title, "items": body.Items, "workspace_id": s.ipc.WorkspaceID, "agent_id": agent, "run_id": run, "operation_key": key})
	if err != nil {
		writeJSONResponse(w, 400, map[string]string{"error": "invalid proposal"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost, "/api/v1/internal/chat-proposals", "proposal create", raw)
}

func (s *Server) handleProposalEdit(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, 503, map[string]string{"error": "IPC not configured"})
		return
	}
	var body struct {
		chatentities.ProposalEdit
		OperationKey string `json:"operation_key"`
	}
	if !decodeCappedJSON(w, r, &body) {
		return
	}
	agent, _, run, present, matched := s.actingRunIdentity(r)
	if !present || !matched || run == "" {
		writeJSONResponse(w, 403, map[string]string{"error": "verified run required"})
		return
	}
	_, key, ok := s.issueOperationOrigin(w, r, body.OperationKey)
	if !ok {
		return
	}
	if strings.TrimSpace(key) == "" {
		writeJSONResponse(w, 400, map[string]string{"error": "operation_key_required"})
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/chat/proposal/"), "/edit")
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#") {
		writeJSONResponse(w, 400, map[string]string{"error": "invalid proposal id"})
		return
	}
	for _, item := range body.Items {
		if item.CrewID != s.ipc.CrewID {
			writeJSONResponse(w, 404, map[string]string{"error": "Card not found"})
			return
		}
	}
	raw, err := json.Marshal(map[string]any{"expected_version": body.ExpectedVersion, "title": body.Title, "items": body.Items, "workspace_id": s.ipc.WorkspaceID, "agent_id": agent, "run_id": run, "operation_key": key})
	if err != nil {
		writeJSONResponse(w, 400, map[string]string{"error": "invalid proposal"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPatch, "/api/v1/internal/chat-proposals/"+url.PathEscape(id), "proposal edit", raw)
}
