package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func (s *Server) handleMooring(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil || !s.ipc.GridCraftEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.documentDraftIdentity(w, r) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 0 {
		http.Error(w, "application scope belongs in the retained packet", 400)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	raw, err := io.ReadAll(io.LimitReader(r.Body, appengine.MaxSnapshotBytes+16385))
	if err != nil || len(raw) > appengine.MaxSnapshotBytes+16384 {
		http.Error(w, "application packet exceeds limit", 413)
		return
	}
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		http.Error(w, "live run capability refused", 403)
		return
	}
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/mooring", raw, appengine.MaxSnapshotBytes+16384, slug)
	if err != nil {
		http.Error(w, "application outcome unavailable; retain the exact packet", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

// One tool dispatches any installed manifest command; application discovery is
// returned under the retained document mandate, never a caller-supplied schema.
func mooringMCPCatalog() []memoryMCPToolDescriptor {
	text := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 1}
	object := func(props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	packet := object(map[string]any{
		"mutation_id": text, "base_rev": integer, "generation": integer,
		"command": text, "arguments": map[string]any{"type": "object"},
		"read_set": workbookReadSetMCPSchema(),
	}, []string{"mutation_id", "base_rev", "generation", "command", "arguments"})
	schema, _ := json.Marshal(object(map[string]any{
		"action":      map[string]any{"type": "string", "enum": []string{"read", "command"}},
		"artifact_id": text, "mandate_id": text, "request": packet,
	}, []string{"action", "artifact_id", "mandate_id"}))
	return []memoryMCPToolDescriptor{{Name: "mooring_command", Description: "Read or run a manifest-declared application command under an explicit human mandate for this live run. Read discovers the manifest and state. Preserve mutation ID and preconditions on uncertain retry. Shared DIRECT documents are refused.", InputSchema: schema}}
}
func (s *Server) respondMooringMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	if name != "mooring_command" {
		return false
	}
	if s.ipc == nil || !s.ipc.GridCraftEnabled {
		s.writeRoutinesMCPToolResult(w, req, 404, mustJSON(map[string]string{"error": "application unavailable"}))
		return true
	}
	if len(raw) > appengine.MaxSnapshotBytes+16384 || !json.Valid(raw) {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid application packet"}))
		return true
	}
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		s.writeRoutinesMCPToolResult(w, req, 403, mustJSON(map[string]string{"error": "live run capability required"}))
		return true
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return true
	}
	defer release()
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/mooring", raw, appengine.MaxSnapshotBytes+16384, slug)
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "outcome unknown; retain exact packet"}))
		return true
	}
	s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	return true
}
