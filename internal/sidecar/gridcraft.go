package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func (s *Server) handleGridCraft(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil || !s.ipc.GridCraftEnabled {
		http.NotFound(w, r)
		return
	}
	if !s.documentDraftIdentity(w, r) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 0 {
		http.Error(w, "workbook scope belongs in the retained packet", 400)
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
		http.Error(w, "workbook packet exceeds limit", 413)
		return
	}
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		http.Error(w, "live run capability refused", 403)
		return
	}
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/gridcraft", raw, appengine.MaxSnapshotBytes+16384, slug)
	if err != nil {
		http.Error(w, "workbook outcome unavailable; retain the exact packet", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
func gridcraftMCPCatalog() []memoryMCPToolDescriptor {
	text := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 1}
	object := func(props map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	command := object(map[string]any{"kind": map[string]any{"const": "cell.set"}, "sheet": text, "cell": text, "input": text}, []string{"kind", "sheet", "cell", "input"})
	packet := object(map[string]any{"mutation_id": text, "mandate_id": text, "base_rev": integer, "generation": integer, "command": command}, []string{"mutation_id", "mandate_id", "base_rev", "generation", "command"})
	result := []memoryMCPToolDescriptor{}
	for _, name := range []string{"workbook_read", "workbook_apply", "workbook_status", "workbook_commits"} {
		props := map[string]any{"artifact_id": text, "mandate_id": text}
		required := []string{"artifact_id", "mandate_id"}
		if name == "workbook_apply" {
			props["request"] = packet
			required = append(required, "request")
		}
		if name == "workbook_status" {
			props["mutation_id"] = text
			required = append(required, "mutation_id")
		}
		description := "Experimental offline workbook operation under an explicit retained human mandate and current run authority. Keep original mutation, mandate, base revision and generation on uncertain retry. Only cell.set is supported; never renew scope or refresh preconditions automatically."
		if name == "workbook_commits" {
			props["since"] = map[string]any{"type": "integer", "minimum": 0}
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": appengine.MaxFeedLimit}
			required = append(required, "since")
			description = "Experimental: list confirmed workbook revisions after since (ascending, at most limit) under the retained human mandate and current run authority. own marks this mandate's revisions; others show only a display label. A 410 snapshot_required means reread the workbook. Reading never widens the mandate."
		}
		schema, _ := json.Marshal(object(props, required))
		result = append(result, memoryMCPToolDescriptor{Name: name, Description: description, InputSchema: schema})
	}
	return result
}
func (s *Server) respondGridCraftMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	action := ""
	switch name {
	case "workbook_read":
		action = "read"
	case "workbook_apply":
		action = "apply"
	case "workbook_status":
		action = "status"
	case "workbook_commits":
		action = "commits"
	default:
		return false
	}
	if s.ipc == nil || !s.ipc.GridCraftEnabled {
		s.writeRoutinesMCPToolResult(w, req, 404, mustJSON(map[string]string{"error": "workbook unavailable"}))
		return true
	}
	var args struct {
		ArtifactID string             `json:"artifact_id"`
		MandateID  string             `json:"mandate_id"`
		MutationID string             `json:"mutation_id,omitempty"`
		Request    *appengine.Request `json:"request,omitempty"`
		Since      *int64             `json:"since,omitempty"`
		Limit      int                `json:"limit,omitempty"`
	}
	if appengine.DecodeStrict(raw, &args) != nil || args.ArtifactID == "" || args.MandateID == "" {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid workbook packet"}))
		return true
	}
	if action == "apply" && (args.Request == nil || args.Request.Command.Kind != "cell.set") || action != "apply" && args.Request != nil || action == "status" && args.MutationID == "" || action != "status" && args.MutationID != "" {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid workbook operation"}))
		return true
	}
	feed := args.Since != nil || args.Limit != 0
	if action == "commits" && (args.Since == nil || *args.Since < 0 || args.Limit < 0 || args.Limit > appengine.MaxFeedLimit) || action != "commits" && feed {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid workbook feed request"}))
		return true
	}
	payload := map[string]any{"action": action, "artifact_id": args.ArtifactID, "mandate_id": args.MandateID}
	if args.Since != nil {
		payload["since"] = *args.Since
	}
	if args.Limit != 0 {
		payload["limit"] = args.Limit
	}
	if args.Request != nil {
		payload["request"] = args.Request
	}
	if args.MutationID != "" {
		payload["mutation_id"] = args.MutationID
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
	body, _ := json.Marshal(payload)
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/gridcraft", body, appengine.MaxSnapshotBytes+16384, slug)
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "workbook outcome unknown; retain the exact packet"}))
		return true
	}
	s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	return true
}
