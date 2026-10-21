package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// issueOperationOrigin returns only token-bound provenance. A v2-enabled crew
// cannot bypass human authority by selecting a boot-time v1 token instead.
// Legacy crews retain the existing actingAgentID resolution, including the
// tokenless boot identity only when no agent tokens have been provisioned.
func (s *Server) issueOperationOrigin(w http.ResponseWriter, r *http.Request, bodyKey string) (string, string, bool) {
	_, _, runID, present, matched := s.actingRunIdentity(r)
	if s.ipc != nil && s.ipc.AgentRunKey != "" {
		if !present || (matched && runID == "") {
			writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "legacy_token_mutation_refused"})
			return "", "", false
		}
		if !matched {
			writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
			return "", "", false
		}
	} else if _, ok := s.actingAgentID(r); !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return "", "", false
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && bodyKey != "" && key != bodyKey {
		writeJSONResponse(w, http.StatusConflict, map[string]string{"error": "operation_key_conflict"})
		return "", "", false
	}
	if key == "" {
		key = bodyKey
	}
	if len(key) > 200 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "operation_invalid"})
		return "", "", false
	}
	if runID == "" {
		// Legacy crews have no durable run scope for idempotency keys.
		key = ""
	}
	return runID, key, true
}

// refuseUnauthorizedIssueMutation covers every currently exposed mutating issue
// handler through issueActor. Unsupported v2 verbs fail closed until their host
// handler atomically enforces requester authority, idempotence and receipts.
// A separate preflight would leave a gap between authorization and mutation.
func (s *Server) refuseUnauthorizedIssueMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || s.ipc == nil || s.ipc.AgentRunKey == "" {
		return false
	}
	agentID, _, runID, present, matched := s.actingRunIdentity(r)
	if !present || (matched && runID == "") {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "legacy_token_mutation_refused"})
		return true
	}
	if !matched {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return true
	}
	supported := r.Method == http.MethodPost && (r.URL.Path == "/issue/create" || strings.HasSuffix(r.URL.Path, "/comment") || strings.HasSuffix(r.URL.Path, "/show"))
	if r.Method == http.MethodPatch {
		ident := strings.TrimPrefix(r.URL.Path, "/issue/")
		supported = ident != "" && !strings.Contains(ident, "/")
	}
	if !supported {
		return s.refuseHumanIssueMutation(w, r, agentID, runID)
	}
	return false
}

// This host query classifies a durable run; it does not approve a mutation.
// Autonomous work retains its existing issue verbs. Human-requested work must
// use a host path that enforces its receipt and mutation in one transaction.
func (s *Server) refuseHumanIssueMutation(w http.ResponseWriter, r *http.Request, agentID, runID string) bool {
	payload, err := json.Marshal(map[string]string{"workspace_id": s.ipc.WorkspaceID, "agent_id": agentID, "author_run_id": runID})
	if err != nil {
		writeJSONResponse(w, 503, map[string]string{"error": "run_authority_unavailable"})
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ipc.BaseURL+"/api/v1/internal/issues/authority", bytes.NewReader(payload))
	if err != nil {
		writeJSONResponse(w, 503, map[string]string{"error": "run_authority_unavailable"})
		return true
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", s.ipc.Token)
	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, 503, map[string]string{"error": "run_authority_unavailable"})
		return true
	}
	defer resp.Body.Close()
	var result struct {
		Mode  string `json:"mode"`
		Error string `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		writeJSONResponse(w, 503, map[string]string{"error": "run_authority_unavailable"})
		return true
	}
	if resp.StatusCode == http.StatusOK && result.Mode == "autonomous" {
		return false
	}
	if resp.StatusCode == http.StatusOK && result.Mode == "human" {
		writeJSONResponse(w, 403, map[string]string{"error": "chat_issue_action_not_supported"})
		return true
	}
	if resp.StatusCode == http.StatusForbidden && result.Error != "" {
		writeJSONResponse(w, 403, map[string]string{"error": result.Error})
		return true
	}
	writeJSONResponse(w, 503, map[string]string{"error": "run_authority_unavailable"})
	return true
}
