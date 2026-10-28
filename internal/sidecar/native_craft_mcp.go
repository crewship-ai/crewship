package sidecar

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// A tool cannot select a Page, actor, run, mandate, engine or host URL. The
// host resolves the authenticated run's one confirmed whole-document grant.
func (s *Server) handleNativeCraftMCP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid || s.ipc == nil {
		writeJSONResponse(w, 403, map[string]string{"error": "live native Craft task required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (128<<10)+1))
	var req memoryMCPRequest
	if err != nil || len(raw) > 128<<10 || decodeMooringMCP(raw, &req) != nil || req.JSONRPC != "2.0" {
		writeJSONResponse(w, 400, memoryMCPResponse{JSONRPC: "2.0", ID: mcpNullID, Error: &memoryMCPRPCError{Code: -32600, Message: "invalid bounded request"}})
		return
	}
	switch req.Method {
	case "initialize":
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{"protocolVersion": MemoryMCPProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "crewship-craft", "version": "1.0.0"}})})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		tools := []memoryMCPToolDescriptor{}
		for _, op := range []string{"read", "acquire", "renew", "apply", "release"} {
			tools = append(tools, memoryMCPToolDescriptor{Name: "craft_" + op, Description: nativeCraftToolDescription(op), InputSchema: nativeCraftToolSchema(op)})
		}
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{"tools": tools})})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if decodeMooringMCP(req.Params, &params) != nil || (len(params.Meta) > 0 && bytes.TrimSpace(params.Meta)[0] != '{') {
			s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid arguments"}))
			return
		}
		packet, ok := nativeCraftToolPacket(params.Name, params.Arguments)
		if !ok {
			s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "explicit bounded native operation required"}))
			return
		}
		response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/craft-native/task", packet, 2<<20, slug)
		if err != nil || response == nil || !json.Valid(response.body) {
			// An uncertain transport result is never Saved and never retried here.
			s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]any{"error": "native task outcome uncertain; retain exact original request", "request": json.RawMessage(packet)}))
			return
		}
		s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	default:
		writeJSONResponse(w, 200, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Error: &memoryMCPRPCError{Code: -32601, Message: "method not found"}})
	}
}

func nativeCraftToolPacket(name string, args json.RawMessage) ([]byte, bool) {
	op := strings.TrimPrefix(name, "craft_")
	if name != "craft_"+op {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if len(args) > 96<<10 || decodeMooringMCP(args, &fields) != nil || fields == nil {
		return nil, false
	}
	allowed := map[string]bool{}
	switch op {
	case "read":
		allowed["query"] = true
	case "acquire":
	case "renew", "release":
		allowed["generation"] = true
	case "apply":
		for _, key := range []string{"generation", "base_version", "request_id", "commands"} {
			allowed[key] = true
		}
	default:
		return nil, false
	}
	for key := range fields {
		if !allowed[key] {
			return nil, false
		}
	}
	if op == "renew" || op == "release" || op == "apply" {
		if !nativeCraftPositive(fields["generation"]) {
			return nil, false
		}
	}
	if op == "apply" {
		var id string
		if !nativeCraftPositive(fields["base_version"]) || json.Unmarshal(fields["request_id"], &id) != nil || len(id) == 0 || len(id) > 128 {
			return nil, false
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
				return nil, false
			}
		}
		var commands []json.RawMessage
		if len(fields["commands"]) > 64<<10 || json.Unmarshal(fields["commands"], &commands) != nil || len(commands) == 0 || len(commands) > 64 {
			return nil, false
		}
	}
	if query := fields["query"]; len(query) > 0 {
		if len(query) > 64<<10 || bytes.TrimSpace(query)[0] != '{' {
			return nil, false
		}
	}
	// Retain original argument bytes and original request_id. Operation is host
	// selected by the fixed tool name, never a model-supplied selector.
	trimmed := bytes.TrimSpace(args)
	packet := []byte(`{"operation":"` + op + `"`)
	if len(fields) > 0 {
		packet = append(packet, ',')
		packet = append(packet, trimmed[1:len(trimmed)-1]...)
	}
	packet = append(packet, '}')
	return packet, true
}
func nativeCraftPositive(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return err == nil && number > 0 && strconv.FormatInt(number, 10) == value
}
func nativeCraftToolDescription(op string) string {
	switch op {
	case "read":
		return "Read this confirmed task's native document version and bounded original-engine query. Word query {command:text,params:{}}; Sheet query {command:cell.get,params:{cell:A1}}. Read never acquires a lease."
	case "acquire":
		return "Acquire the same document lease under this agent's own identity. A human holder is refused. Retain the returned generation; never borrow a human session."
	case "renew":
		return "Renew this agent's exact current generation. Expired, revoked or handed-off authority is refused."
	case "release":
		return "Release this agent's exact current generation after inspecting the save receipt."
	default:
		return "Apply allowlisted original-engine commands using exact generation, base_version and a new request_id. Word caret.docStart/docEnd params{} and text.insert params{text}; Sheet cell.set params{cell,input} accepts scalar literals; formulas are unavailable in this initial tool. Only a committed Node receipt proves Saved. Retain the exact original request for uncertain reconciliation; never blindly retry or change its base."
	}
}
func nativeCraftToolSchema(op string) json.RawMessage {
	props := map[string]any{}
	required := []string{}
	positive := map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$", "maxLength": 19}
	if op == "renew" || op == "release" || op == "apply" {
		props["generation"] = positive
		required = append(required, "generation")
	}
	command := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"command", "params"}, "properties": map[string]any{"command": map[string]any{"type": "string", "enum": []string{"caret.docStart", "caret.docEnd", "text.insert", "cell.set", "text", "cell.get"}}, "params": map[string]any{"type": "object"}}}
	if op == "read" {
		props["query"] = command
	}
	if op == "apply" {
		props["base_version"] = positive
		props["request_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_.:-]+$"}
		props["commands"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": command}
		required = append(required, "base_version", "request_id", "commands")
	}
	return mustJSON(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
}
