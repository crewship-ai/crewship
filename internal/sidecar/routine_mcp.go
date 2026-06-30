package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
)

// RoutinesMCPServerName is the server identity the in-container CLI sees in
// its tool list for the routine-authoring tools. Kept short + branded so a
// tool-call trace makes it obvious the save_routine / list_routines tools
// came from Crewship's sidecar, not a user-declared MCP server.
//
// MUST match orchestrator.RoutinesMCPServerName — both sides hard-code the
// string rather than share an import to avoid an orchestrator→sidecar cycle.
const RoutinesMCPServerName = "crewship-routines"

// routineMCPSaveSchema is the JSON Schema (Draft 2020-12) for save_routine.
// Field name parity with pipelinesSaveRequest: name/description/definition/
// sample_inputs. `definition` is the routine DSL object; `sample_inputs`
// feeds the mandatory test_run gate that runs before the routine is saved.
var routineMCPSaveSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Human-readable routine name. The url slug is derived from this server-side."
		},
		"description": {
			"type": "string",
			"description": "One-line summary of what the routine does."
		},
		"definition": {
			"type": "object",
			"description": "The routine DSL definition (steps, inputs, schedules, etc.). Validated by an inline test_run before the routine is persisted."
		},
		"sample_inputs": {
			"type": "object",
			"description": "Example inputs supplied to the mandatory test_run. Pick values that exercise the routine end-to-end so the gate passes.",
			"additionalProperties": true
		}
	},
	"required": ["name", "definition"],
	"additionalProperties": false
}`)

// routineMCPRunSchema is the JSON Schema (Draft 2020-12) for run_routine.
// `slug` identifies the saved routine (as returned by list_routines / shown in
// the [AVAILABLE ROUTINES] block); `inputs` is the routine's input object.
// Workspace + invoker identity are derived from the sidecar IPC config, never
// the caller.
var routineMCPRunSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"slug": {
			"type": "string",
			"description": "The slug of the saved routine to run (from list_routines or the [AVAILABLE ROUTINES] block)."
		},
		"inputs": {
			"type": "object",
			"description": "Input values for the routine, matching its declared inputs.",
			"additionalProperties": true
		}
	},
	"required": ["slug"],
	"additionalProperties": false
}`)

// routineMCPListSchema is the schema for the read-only list_routines tool.
// It takes no arguments — workspace scope is derived from the sidecar IPC
// config, never from the caller.
var routineMCPListSchema = json.RawMessage(`{
	"type": "object",
	"properties": {},
	"additionalProperties": false
}`)

// routineMCPTools is the stable, ordered tool catalog tools/list returns.
// Order is fixed so adapters that snapshot the catalog see a deterministic
// payload (map iteration order is unspecified in Go).
var routineMCPTools = []memoryMCPToolDescriptor{
	{
		Name: "save_routine",
		Description: "Author a Crewship routine (a durable, versioned, schedulable pipeline). " +
			"Supply the routine name, a short description, the DSL `definition` object, and " +
			"`sample_inputs` for the mandatory test_run. The routine is test-run inline before " +
			"saving: on success the saved routine is returned; on a DSL or validation error the " +
			"exact failure is returned so you can fix the definition and call save_routine again. " +
			"Do NOT shell out to curl — call this tool directly.",
		InputSchema: routineMCPSaveSchema,
	},
	{
		Name: "list_routines",
		Description: "List the routines visible to this workspace (the same set a user sees in the " +
			"UI). Use this to check whether a routine already exists before authoring a new one.",
		InputSchema: routineMCPListSchema,
	},
	{
		Name: "run_routine",
		Description: "Run a saved Crewship routine by slug instead of improvising the same work by " +
			"hand. Supply the routine `slug` (from list_routines or the [AVAILABLE ROUTINES] block) and " +
			"an `inputs` object matching its declared inputs. The run executes synchronously and the " +
			"run result/status is returned so you can report the outcome. Do NOT shell out to curl — " +
			"call this tool directly.",
		InputSchema: routineMCPRunSchema,
	},
}

// handleRoutinesMCP is the JSON-RPC 2.0 entry point in-container CLIs hit at
// /mcp/routines. It mirrors handleMemoryMCP exactly — same envelope, same
// localhost gate one level up in buildHandler — but serves the routine-
// authoring tools instead of the memory tools. Methods:
//
//   - initialize  → handshake; returns protocolVersion + serverInfo
//   - tools/list  → returns save_routine + list_routines + run_routine descriptors
//   - tools/call  → dispatches save_routine / list_routines / run_routine to the
//     shared savePipeline / listPipelines / runPipeline helpers (author +
//     invoker identity injected from IPC)
//
// Unknown methods return JSON-RPC -32601 (method not found).
func (s *Server) handleRoutinesMCP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap — MCP requests are tiny
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpNullID,
			Error:   &memoryMCPRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	var req memoryMCPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpNullID,
			Error:   &memoryMCPRPCError{Code: -32700, Message: "invalid JSON: " + err.Error()},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		id := req.ID
		if len(id) == 0 {
			id = mcpNullID
		}
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      id,
			Error:   &memoryMCPRPCError{Code: -32600, Message: "jsonrpc must be \"2.0\""},
		})
		return
	}

	switch req.Method {
	case "initialize":
		s.respondRoutinesMCPInitialize(w, req)
	case "tools/list":
		s.respondRoutinesMCPToolsList(w, req)
	case "tools/call":
		s.respondRoutinesMCPToolsCall(w, r, req)
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusOK)
	default:
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &memoryMCPRPCError{
				Code:    -32601,
				Message: "method not found: " + req.Method,
			},
		})
	}
}

func (s *Server) respondRoutinesMCPInitialize(w http.ResponseWriter, req memoryMCPRequest) {
	result, _ := json.Marshal(map[string]any{
		"protocolVersion": MemoryMCPProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    RoutinesMCPServerName,
			"version": "1.0.0",
		},
	})
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) respondRoutinesMCPToolsList(w http.ResponseWriter, req memoryMCPRequest) {
	result, _ := json.Marshal(map[string]any{"tools": routineMCPTools})
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) respondRoutinesMCPToolsCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &memoryMCPRPCError{
				Code: -32602, Message: "invalid params: " + err.Error(),
			},
		})
		return
	}

	var status int
	var bodyBytes []byte
	switch params.Name {
	case "save_routine":
		var save pipelinesSaveRequest
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &save); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.savePipeline(r.Context(), save)
	case "list_routines":
		status, bodyBytes = s.listPipelines(r.Context(), "")
	case "run_routine":
		var run routineRunRequest
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &run); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.runPipeline(r.Context(), run)
	default:
		// Unknown tool — surface as a recoverable MCP tool error (isError)
		// so the model can correct the name and retry, matching the memory
		// dispatcher's recoverable-vs-fatal split.
		s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
			mustJSON(map[string]string{"error": "unknown tool: " + params.Name}))
		return
	}
	s.writeRoutinesMCPToolResult(w, req, status, bodyBytes)
}

// writeRoutinesMCPToolResult wraps a shared-helper (status, body) pair in an
// MCP tools/call result. status >= 400 maps to isError=true — the same
// recoverable signal MCP clients (Claude/Codex/Gemini/OpenCode/Droid)
// surface back to the model as a tool_result with is_error, so a failed
// test_run becomes a fix-and-retry prompt rather than a hard stop.
func (s *Server) writeRoutinesMCPToolResult(w http.ResponseWriter, req memoryMCPRequest, status int, body []byte) {
	out := memoryMCPToolCallResult{
		Content: []memoryMCPToolCallContent{{Type: "text", Text: string(body)}},
		IsError: status >= 400,
	}
	result, _ := json.Marshal(out)
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}
