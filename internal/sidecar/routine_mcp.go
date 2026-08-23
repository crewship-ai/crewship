package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest"
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
		},
		"crew": {
			"type": "string",
			"description": "Slug of the crew this routine is FOR. Onboarding only: the setup guide builds routines that belong to the crew the person just created, and must name it here. That crew must already exist. Ordinary crews leave this unset — a routine belongs to the crew that writes it."
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

// routineMCPDiscoverSchema is the schema for the read-only
// discover_capabilities tool. Takes no arguments — the crew is the sidecar's
// own crew, derived from IPC, never the caller.
var routineMCPDiscoverSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"crew": {
			"type": "string",
			"description": "Slug of the crew to describe. Onboarding only: pass the crew you are authoring FOR, so the agent slugs you get back are the ones your routine or page must name. Omit to describe your own crew."
		}
	},
	"additionalProperties": false
}`)

// pagesMCPSaveSchema is the JSON Schema for save_page. Panels stays a bare
// array of objects rather than a fully-specified schema: the server is the
// real validator (documentFrom/resolveReferences), and duplicating its
// rules here would only give this schema a second copy to drift from.
var pagesMCPSaveSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Human-readable page name. The url slug is derived from this server-side."
		},
		"description": {
			"type": "string",
			"description": "One-line summary of what the page shows."
		},
		"panels": {
			"type": "array",
			"description": "The page's panels. Each needs id, schema (one of status.v1, metric.v1, series.v1, table.v1, narrative.v1, embed.v1), owner (\"crew/<slug>\"), producer (\"<kind>/<ref>\", e.g. \"agent/<slug>\" or \"routine/<slug>\"), sla_seconds, and span (1-12). Call discover_capabilities first to see this crew's real agent/routine slugs before naming a producer.",
			"items": {"type": "object"}
		},
		"crew": {
			"type": "string",
			"description": "Slug of the crew this page is FOR. Onboarding only: the setup guide builds pages that belong to the crew the person just created, and must name it here — its panels' owner and producer refs must name that crew and ITS agents, which discover_capabilities will list if you pass the same slug. Ordinary crews leave this unset."
		}
	},
	"required": ["name", "panels"],
	"additionalProperties": false
}`)

// manifestMCPValidateSchema accepts the exact YAML stream a human would pass
// to `crewship apply --file`. The tool is validation-only: keeping mutation
// out of this surface means an agent-authored document cannot bypass the
// browser/session confirmation an eventual manifest proposal card requires.
var manifestMCPValidateSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"yaml": {
			"type": "string",
			"description": "One or more --- separated crewship/v1 YAML documents to parse and validate."
		}
	},
	"required": ["yaml"],
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
			"A routine belongs to the crew that runs it: if you are building for a DIFFERENT crew " +
			"(the onboarding guide always is), pass its slug as `crew` — the routine's network " +
			"policy, credentials and container all follow that ownership. " +
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
	{
		Name: "save_page",
		Description: "Create a Crewship page (a typed operational dashboard: status/metric/series/table/narrative/embed " +
			"panels). Supply the page name, a short description, and the `panels` array. If this crew's autonomy level " +
			"allows it the page is created immediately and its document is returned; otherwise the request is HELD for " +
			"operator approval (no page is created) and you must tell the user that plainly rather than claiming the " +
			"page exists. On a validation error the exact failure is returned so you can fix the panels and call " +
			"save_page again. If you are building for a DIFFERENT crew (the onboarding guide always is), pass its " +
			"slug as `crew`, and give discover_capabilities the same slug so the producer refs you write name that " +
			"crew's real agents. Do NOT shell out to curl — call this tool directly.",
		InputSchema: pagesMCPSaveSchema,
	},
	{
		Name: "discover_capabilities",
		Description: "Return, in ONE bundle, everything needed to author a valid routine for THIS crew: " +
			"the routine DSL JSON schema, the crew's container capabilities (datastores + installed CLIs), " +
			"connected integrations WITH their enabled tool names, the crew's agent slugs (for agent_run " +
			"steps), and the runtimes actually wired in this build (type: code expr/cel; type: script " +
			"interpreters). Call this FIRST when authoring a routine so save_routine passes on the first " +
			"try — do not guess agent slugs, tool names, or runtimes.",
		InputSchema: routineMCPDiscoverSchema,
	},
	{
		Name: "validate_manifest",
		Description: "Validate an authored Crewship YAML manifest with the same parser and offline schemas used by " +
			"`crewship apply`. Supports every current crewship/v1 kind, including Crew, Agent, Routine and Page, " +
			"and accepts multiple `---` separated documents. This tool NEVER writes workspace state. Fix every " +
			"returned error before presenting YAML as ready. A successful validation is not an apply; references " +
			"to state that already exists in the workspace are rechecked when an apply plan is built.",
		InputSchema: manifestMCPValidateSchema,
	},
}

// handleRoutinesMCP is the JSON-RPC 2.0 entry point in-container CLIs hit at
// /mcp/routines. It mirrors handleMemoryMCP exactly — same envelope, same
// localhost gate one level up in buildHandler — but serves the routine-
// authoring tools instead of the memory tools. Methods:
//
//   - initialize  → handshake; returns protocolVersion + serverInfo
//   - tools/list  → returns save_routine + list_routines + run_routine + save_page
//     (+ discover_capabilities + validate_manifest) descriptors
//   - tools/call  → dispatches save_routine / list_routines / run_routine /
//     save_page to the shared savePipeline / listPipelines / runPipeline /
//     savePage helpers (author + invoker identity injected from IPC)
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

	// #812: resolve the ACTING agent from the per-agent bearer token for
	// authorship/invocation attribution. A forged token is refused.
	actingAgentID, idOK := s.actingAgentID(r)
	if !idOK {
		s.writeRoutinesMCPToolResult(w, req, http.StatusForbidden,
			mustJSON(map[string]string{"error": "unrecognized agent token"}))
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
		status, bodyBytes = s.savePipeline(r.Context(), save, actingAgentID)
	case "save_page":
		var save pagesSaveRequest
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &save); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.savePage(r.Context(), save, actingAgentID)
	case "list_routines":
		status, bodyBytes = s.listPipelines(r.Context(), "")
	case "discover_capabilities":
		var disco struct {
			Crew string `json:"crew"`
		}
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &disco); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.crewCapabilities(r.Context(), disco.Crew)
	case "run_routine":
		var run routineRunRequest
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &run); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.runPipeline(r.Context(), run, actingAgentID)
	case "validate_manifest":
		var input struct {
			YAML string `json:"yaml"`
		}
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &input); err != nil {
				s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		if strings.TrimSpace(input.YAML) == "" {
			status, bodyBytes = http.StatusBadRequest, mustJSON(map[string]string{"error": "yaml is required"})
			break
		}
		bundle, err := manifest.Load([]byte(input.YAML))
		if err == nil {
			err = manifest.ValidateBundle(bundle)
		}
		if err != nil {
			status, bodyBytes = http.StatusBadRequest, mustJSON(map[string]string{
				"error": "manifest validation failed: " + err.Error(),
			})
			break
		}
		status, bodyBytes = http.StatusOK, mustJSON(map[string]any{
			"valid":   true,
			"message": "Manifest is structurally valid. No workspace state was changed.",
		})
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
