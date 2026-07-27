package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// NotifyMCPServerName is the canonical name every adapter advertises for the
// in-container notification MCP server. MUST match
// internal/orchestrator.NotifyMCPServerName — both sides hard-code the string
// rather than share an import, to avoid an orchestrator→sidecar cycle.
const NotifyMCPServerName = "crewship-notify"

var notifyMCPSendSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"channel_id": {
			"type": "string",
			"description": "The channel to send to, from list_notification_channels."
		},
		"title": {
			"type": "string",
			"description": "One-line summary. This is all a phone notification shows."
		},
		"body": {
			"type": "string",
			"description": "Optional detail, markdown. Max 8KB."
		}
	},
	"required": ["channel_id", "title"],
	"additionalProperties": false
}`)

var notifyMCPListSchema = json.RawMessage(`{
	"type": "object",
	"properties": {},
	"additionalProperties": false
}`)

// notifyMCPTools is the stable, ordered catalog tools/list returns.
//
// The descriptions carry two things the model would otherwise get wrong.
// First, that a channel has to be granted by a human — an agent that reads
// "send a notification" and gets a 403 will otherwise retry, reword, or try
// to shell out. Second, that these messages land where PEOPLE are: a Slack
// channel a team reads, or someone's phone at 2am. A tool description is the
// only place that context reaches the model.
var notifyMCPTools = []memoryMCPToolDescriptor{
	{
		Name: "list_notification_channels",
		Description: "List the notification channels THIS agent is allowed to send to (Slack, Discord, " +
			"Telegram, email, webhook, …). Call this FIRST — an agent has no channels by default, and a " +
			"human must pair each one explicitly. If the list is empty, say so rather than guessing a " +
			"channel id.",
		InputSchema: notifyMCPListSchema,
	},
	{
		Name: "notify_send",
		Description: "Send a notification to one of the channels from list_notification_channels. " +
			"These messages reach PEOPLE — a team's Slack channel, or someone's phone — so send when " +
			"there is something a human actually needs to know (a long job finished, something needs a " +
			"decision, something broke), not as progress commentary. Rate limited per channel; a 429 " +
			"means you are sending too often, not that you should retry. Do NOT shell out to curl — " +
			"call this tool directly.",
		InputSchema: notifyMCPSendSchema,
	},
}

type notifySendArgs struct {
	ChannelID string `json:"channel_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// handleNotifyMCP is the JSON-RPC 2.0 entry point in-container CLIs hit at
// /mcp/notify. Mirrors handleRoutinesMCP exactly — same envelope, same
// localhost gate one level up in buildHandler — serving the notification
// tools instead.
//
// Identity comes from IPC and the per-agent bearer token, never from the
// request body: the agent id is what the server's pairing check keys on, so
// letting a caller name its own would make the whole grant model decorative.
func (s *Server) handleNotifyMCP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0", ID: mcpNullID,
			Error: &memoryMCPRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	var req memoryMCPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0", ID: mcpNullID,
			Error: &memoryMCPRPCError{Code: -32700, Message: "invalid JSON: " + err.Error()},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		id := req.ID
		if len(id) == 0 {
			id = mcpNullID
		}
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0", ID: id,
			Error: &memoryMCPRPCError{Code: -32600, Message: "jsonrpc must be \"2.0\""},
		})
		return
	}

	switch req.Method {
	case "initialize":
		result, _ := json.Marshal(map[string]any{
			"protocolVersion": MemoryMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": NotifyMCPServerName, "version": "1.0.0"},
		})
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	case "tools/list":
		result, _ := json.Marshal(map[string]any{"tools": notifyMCPTools})
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	case "tools/call":
		s.respondNotifyMCPToolsCall(w, r, req)
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusOK)
	default:
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &memoryMCPRPCError{Code: -32601, Message: "method not found: " + req.Method},
		})
	}
}

func (s *Server) respondNotifyMCPToolsCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &memoryMCPRPCError{Code: -32602, Message: "invalid params: " + err.Error()},
		})
		return
	}
	if s.ipc == nil {
		s.writeNotifyMCPToolResult(w, req, http.StatusServiceUnavailable,
			mustJSON(map[string]string{"error": "notifications unavailable: IPC not configured"}))
		return
	}
	actingAgentID, idOK := s.actingAgentID(r)
	if !idOK {
		s.writeNotifyMCPToolResult(w, req, http.StatusForbidden,
			mustJSON(map[string]string{"error": "unrecognized agent token"}))
		return
	}

	var status int
	var bodyBytes []byte
	switch params.Name {
	case "list_notification_channels":
		status, bodyBytes = s.listNotifyChannels(r.Context(), actingAgentID)
	case "notify_send":
		var args notifySendArgs
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				s.writeNotifyMCPToolResult(w, req, http.StatusBadRequest,
					mustJSON(map[string]string{"error": "invalid arguments: " + err.Error()}))
				return
			}
		}
		status, bodyBytes = s.sendNotification(r.Context(), args, actingAgentID)
	default:
		s.writeNotifyMCPToolResult(w, req, http.StatusBadRequest,
			mustJSON(map[string]string{"error": "unknown tool: " + params.Name}))
		return
	}
	s.writeNotifyMCPToolResult(w, req, status, bodyBytes)
}

// writeNotifyMCPToolResult wraps a (status, body) pair in an MCP tools/call
// result. status >= 400 maps to isError=true, so a "not paired" refusal
// reaches the model as a recoverable tool error it can report to the user
// rather than a hard stop it retries blindly.
func (s *Server) writeNotifyMCPToolResult(w http.ResponseWriter, req memoryMCPRequest, status int, body []byte) {
	out := memoryMCPToolCallResult{
		Content: []memoryMCPToolCallContent{{Type: "text", Text: string(body)}},
		IsError: status >= 400,
	}
	result, _ := json.Marshal(out)
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

// listNotifyChannels forwards to the internal discovery endpoint.
func (s *Server) listNotifyChannels(ctx context.Context, agentID string) (int, []byte) {
	path := "/api/v1/internal/notifications/channels" +
		"?workspace_id=" + url.QueryEscape(s.ipc.WorkspaceID) +
		"&crew_id=" + url.QueryEscape(s.ipc.CrewID) +
		"&agent_id=" + url.QueryEscape(agentID)
	res, err := s.ipcRequestJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return http.StatusBadGateway, mustJSON(map[string]string{"error": "channel list request failed: " + err.Error()})
	}
	return res.status, res.body
}

// sendNotification forwards to the internal send endpoint. Workspace, crew
// and agent come from IPC + the bearer token, never from the model.
func (s *Server) sendNotification(ctx context.Context, args notifySendArgs, agentID string) (int, []byte) {
	if args.ChannelID == "" {
		return http.StatusBadRequest, mustJSON(map[string]string{
			"error": "channel_id required — call list_notification_channels first",
		})
	}
	if args.Title == "" {
		return http.StatusBadRequest, mustJSON(map[string]string{"error": "title required"})
	}
	body, err := json.Marshal(map[string]any{
		"workspace_id": s.ipc.WorkspaceID,
		"crew_id":      s.ipc.CrewID,
		"agent_id":     agentID,
		"channel_id":   args.ChannelID,
		"title":        args.Title,
		"body":         args.Body,
	})
	if err != nil {
		return http.StatusInternalServerError, mustJSON(map[string]string{"error": "marshal send body"})
	}
	res, err := s.ipcRequestJSON(ctx, http.MethodPost, "/api/v1/internal/notifications/send", body)
	if err != nil {
		return http.StatusBadGateway, mustJSON(map[string]string{"error": "notification send failed: " + err.Error()})
	}
	return res.status, res.body
}
