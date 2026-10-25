package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// This surface has no document selector, alternate actor or generic host URL.
// The host resolves the actual run's one retained, confirmed task and checks
// its original consent and current Page rights for every operation.
func (s *Server) handleMooringMCP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid || s.ipc == nil {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "live document task required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (256<<10)+1))
	var req memoryMCPRequest
	if err != nil || len(raw) > 256<<10 || decodeMooringMCP(raw, &req) != nil || req.JSONRPC != "2.0" {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{JSONRPC: "2.0", ID: mcpNullID, Error: &memoryMCPRPCError{Code: -32600, Message: "invalid bounded request"}})
		return
	}
	switch req.Method {
	case "initialize":
		result := mustJSON(map[string]any{"protocolVersion": MemoryMCPProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "crewship-document", "version": "1.0.0"}})
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		tools := []memoryMCPToolDescriptor{
			{Name: "doc_read", Description: "Read only this confirmed live task's document. {} returns the current whole D1 command view. {page:{}} starts a bounded immutable D1/D3 read; continue with the returned cursor and exact revision until next_cursor is absent. max_bytes bounds the complete ReadResult JSON (1024–262144); the MCP envelope is separately capped at 2 MiB. Native JSON is losslessly encoded in each item's data.native_json string. An expired or replaced cursor requires a new read. D3 is read-only. Retain original uncertain submissions unchanged.", InputSchema: json.RawMessage(mooringReadToolSchema)},
			{Name: "doc_submit", Description: "Submit explicit typed text.insert/delete/format operations using an unchanged doc_read view, mutation_id and changes[].operations[] with explicit read_preconditions[] and dependencies[]. The sidecar deterministically computes the existing protocol fingerprints without rereading. Retain the returned original submission for uncertain retry; retry with submission unchanged. At most 256 operations per original group; larger tasks use multiple groups. Only the actual host outcome receipt establishes Saved.", InputSchema: mooringSubmitToolSchema},
		}
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{"tools": tools})})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if decodeMooringMCP(req.Params, &params) != nil {
			s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid arguments"}))
			return
		}
		method := http.MethodGet
		var path string
		var body []byte
		pagedRead := false
		switch params.Name {
		case "doc_read":
			path, pagedRead, err = mooringReadToolPath(params.Arguments)
			if err != nil {
				s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "read accepts {} or a bounded page object"}))
				return
			}
		case "doc_submit":
			var fields map[string]json.RawMessage
			if decodeMooringMCP(params.Arguments, &fields) != nil {
				s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid original submission"}))
				return
			}
			if _, retry := fields["submission"]; retry {
				var args struct {
					Submission json.RawMessage `json:"submission"`
				}
				if decodeMooringMCP(params.Arguments, &args) != nil || len(args.Submission) == 0 || args.Submission[0] != '{' {
					s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "original submission required"}))
					return
				}
				body = args.Submission
			} else {
				body, err = prepareMooringToolPacket(params.Arguments)
				if err != nil {
					s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "explicit original view and typed changes required"}))
					return
				}
			}
			method, path = http.MethodPost, "/api/v1/internal/mooring/task"
		default:
			s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "unknown document tool"}))
			return
		}
		response, err := s.sendBoundedHostRequest(r, method, path, body, 2<<20, slug)
		if err != nil || response == nil || !json.Valid(response.body) {
			result := map[string]any{"error": "document task unavailable; retain exact original"}
			if body != nil {
				result["submission"] = json.RawMessage(body)
			}
			s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(result))
			return
		}
		if body != nil {
			s.writeRoutinesMCPToolResult(w, req, response.status, mustJSON(map[string]any{"submission": json.RawMessage(body), "outcome": json.RawMessage(response.body)}))
			return
		}
		if pagedRead && !boundedMooringPageResult(req, response.status, response.body) {
			s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "document read exceeds the MCP envelope budget"}))
			return
		}
		s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	default:
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Error: &memoryMCPRPCError{Code: -32601, Message: "method not found"}})
	}
}

func decodeMooringMCP(raw []byte, out any) error {
	// Reuse the audited duplicate-at-every-depth/UTF-8/closed-object decoder;
	// the caller's smaller 256 KiB transport bound remains mandatory.
	return DecodeDocumentDraft(raw, out)
}
