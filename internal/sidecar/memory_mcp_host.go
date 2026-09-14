package sidecar

// The MCP memory tools, routed to the host's guaranteed ledger.
//
// Until this file the MCP tools (memory.read / memory.write /
// memory.append_daily) always ran the in-container dispatcher: a local file
// write with no revision, no idempotent retry and no run attribution, keyed
// under `agent:<agent id>/<file>` — a second revision history for the same
// file the HTTP route anchors as `agent:<slug>/<file>` on the host. A runtime
// that required the guaranteed profile could only refuse the tools outright.
//
// Now, when the sidecar has a host IPC channel and the call carries a per-run
// capability (the agtv2 bearer the orchestrator injects as
// CREWSHIP_AGENT_TOKEN), the three tools go to the host: the same
// /api/v1/internal/memory/mutation and /canonical routes the HTTP surface
// uses, with the same forwarded capability, the same credential screen, the
// same R5 sent/unknown discipline, and therefore the same revision history.
// The model gets back what it needs to write conditionally — revision,
// content hash, operation and mutation identity, the audit path — as a second
// text content item carrying JSON, so the first item stays what it always was
// (the file body on a read, a one-line summary on a write).
//
// What still runs locally: tiers the host does not serve (PERSONA, peers,
// lessons, memory.search), calls with no run capability (a v1 per-agent token
// or a token-less crew), and a host that is unreachable BEFORE anything was
// sent — the legacy dispatcher is a declared fallback in that last case only,
// and only when the runtime does not require the guarantee. A mutation that
// was sent and not answered is never resolved locally: the tool returns
// memory_mutation_unknown and the remedy is a retry under the same
// operation_id, which the host's ledger answers with the original result.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memory/memdiff"
)

// mcpHostWriteArgs is memory.write's argument shape as the bridge reads it.
// It mirrors memory.writeArgs field for field; the dispatcher's type is not
// exported and the JSON names are the contract.
type mcpHostWriteArgs struct {
	Tier             string            `json:"tier"`
	Key              string            `json:"key"`
	Content          string            `json:"content"`
	Mode             string            `json:"mode"`
	OperationID      string            `json:"operation_id"`
	ExpectedRevision int64             `json:"expected_revision"`
	ExpectedSHA256   string            `json:"expected_sha256"`
	Removals         []memdiff.Removal `json:"removals"`
	FirstWrite       bool              `json:"first_write"`
}

type mcpHostReadArgs struct {
	Tier string `json:"tier"`
	Key  string `json:"key"`
}

type mcpHostAppendDailyArgs struct {
	Entry       string `json:"entry"`
	OperationID string `json:"operation_id"`
	Date        string `json:"date"`
	At          string `json:"at"`
}

// mcpHostTarget maps a tool tier/key onto the host's (scope, file) vocabulary.
// Only the files the host serves map; anything else stays local.
func mcpHostTarget(tier, key string) (scope, file string, ok bool) {
	switch tier {
	case "AGENT":
		return "agent", "AGENT.md", true
	case "CREW":
		return "crew", "CREW.md", true
	case "pins":
		return "agent", "pins.md", true
	case "daily":
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, "/\\") || key == "." || key == ".." {
			return "", "", false
		}
		return "agent", "daily/" + key + ".md", true
	}
	return "", "", false
}

// mcpHostBridgeReady reports whether this call can go to the host at all: an
// IPC channel exists and the caller authenticated with a per-run capability.
// The run id is what the host attributes the write to; without it there is
// nothing to attribute and the local dispatcher is the only honest surface.
func (s *Server) mcpHostBridgeReady(r *http.Request) (runID string, ready bool) {
	if s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.Token == "" {
		return "", false
	}
	_, _, runID, present, ok := s.actingRunIdentity(r)
	if !present || !ok || runID == "" {
		return "", false
	}
	return runID, true
}

// memoryMCPViaHost serves one tools/call through the host when it can.
// handled=false means "not this bridge's call": the caller continues on the
// local path exactly as before this file existed.
func (s *Server) memoryMCPViaHost(r *http.Request, name string, args json.RawMessage) (memoryMCPToolCallResult, bool) {
	switch name {
	case "memory.write", "memory.append_daily", "memory.read":
	default:
		return memoryMCPToolCallResult{}, false
	}
	runID, ready := s.mcpHostBridgeReady(r)
	if !ready {
		return memoryMCPToolCallResult{}, false
	}
	switch name {
	case "memory.read":
		return s.mcpHostRead(r, args)
	case "memory.append_daily":
		var a mcpHostAppendDailyArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return mcpToolError("memory.append_daily", "invalid_args", "invalid args: "+err.Error(), nil), true
		}
		day, line, err := memory.ResolveAppendDaily(a.Entry, a.Date, a.At, func() time.Time { return time.Now().UTC() })
		if err != nil {
			return mcpToolError("memory.append_daily", "invalid_args", err.Error(), nil), true
		}
		return s.mcpHostWrite(r, runID, "memory.append_daily", mcpHostWriteArgs{
			Tier: "daily", Key: day, Content: line, Mode: "append", OperationID: a.OperationID,
		}, map[string]any{"date": day, "at": lineStamp(line)})
	default:
		var a mcpHostWriteArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return mcpToolError("memory.write", "invalid_args", "invalid args: "+err.Error(), nil), true
		}
		return s.mcpHostWrite(r, runID, "memory.write", a, nil)
	}
}

// lineStamp reads the RFC 3339 stamp back out of an append_daily line, so the
// result can hand the caller the exact `at` a retry must repeat.
func lineStamp(line string) string {
	rest := strings.TrimPrefix(line, "- ")
	if i := strings.Index(rest, " — "); i > 0 {
		return rest[:i]
	}
	return ""
}

func (s *Server) mcpHostWrite(r *http.Request, runID, tool string, a mcpHostWriteArgs, extra map[string]any) (memoryMCPToolCallResult, bool) {
	scope, file, ok := mcpHostTarget(a.Tier, a.Key)
	if !ok {
		// A tier the host does not serve (PERSONA, peers) or a malformed daily
		// key: the local dispatcher owns those and answers exactly as before.
		return memoryMCPToolCallResult{}, false
	}
	var op memory.MutateOp
	switch a.Mode {
	case "append":
		op = memory.OpAppend
	case "replace":
		op = memory.OpReplace
	default:
		return mcpToolError(tool, "invalid_args", `mode must be "append" or "replace"`, nil), true
	}
	if strings.TrimSpace(a.Content) == "" {
		return mcpToolError(tool, "invalid_args", "content is required", nil), true
	}
	operationID := strings.TrimSpace(a.OperationID)
	retrySafe := operationID != ""
	if operationID == "" {
		if memoryGuaranteedRequiredByRuntime() {
			return mcpToolError(tool, "operation_id_required",
				"this runtime requires guaranteed memory writes, and a guaranteed write needs a caller-supplied operation_id "+
					"(mint one, e.g. a UUID, and reuse it verbatim on a retry); no write was performed", nil), true
		}
		gen, err := memory.NewOperationID()
		if err != nil {
			return mcpToolError(tool, "internal", err.Error(), nil), true
		}
		operationID = gen
	}
	if op == memory.OpReplace {
		if a.Removals == nil {
			return mcpToolError(tool, "removals_required",
				"a replace must declare its removals (the line spans it deletes in the content you read); "+
					"pass [] when it deletes nothing. No write was performed", nil), true
		}
		if a.ExpectedRevision <= 0 && strings.TrimSpace(a.ExpectedSHA256) == "" && !a.FirstWrite {
			return mcpToolError(tool, "expected_revision_required",
				"a replace needs the revision (or content_sha256) memory.read returned for this file, or first_write: true "+
					"if memory.read returned revision 0. No write was performed", nil), true
		}
	}

	req := MemoryWriteRequest{
		File: file, Content: a.Content, Scope: scope, Mode: a.Mode,
		OperationID: operationID, ExpectedSHA256: a.ExpectedSHA256, Removals: a.Removals,
		ExpectedRevision: a.ExpectedRevision, FirstWrite: a.FirstWrite,
		RunID: runID,
	}
	if reason := s.hostMutationBlocker(r, req, op); reason != "" {
		if memoryGuaranteedRequiredByRuntime() {
			return mcpToolError(tool, "memory_guaranteed_unavailable", reason+"; no write was performed", nil), true
		}
		return memoryMCPToolCallResult{}, false
	}
	if rejection := s.screenBeforeHostMutation(a.Content); rejection != nil {
		s.emitJournal(r.Context(), "memory.write_rejected", "write rejected: "+rejection.Kind,
			map[string]any{"scope": scope, "file": file, "reason": rejection.Kind, "hits": len(rejection.Hits), "operation_id": operationID}, nil)
		return mcpToolError(tool, "memory_write_rejected", rejection.Message, map[string]any{"kind": rejection.Kind, "hits": len(rejection.Hits)}), true
	}

	out := s.mutateOnHost(r, req, op, operationID)
	switch {
	case out.ok:
		meta := map[string]any{
			"profile":           out.res.Profile,
			"revision":          out.res.Revision,
			"content_sha256":    out.res.ContentSHA256,
			"revision_checked":  out.res.LedgerRecorded,
			"bytes_written":     out.res.BytesWritten,
			"operation_id":      operationID,
			"mutation_id":       out.res.MutationID,
			"audit_path":        out.res.AuditPath,
			"base_revision":     out.res.BaseRevision,
			"idempotent":        out.res.Idempotent,
			"removals_verified": out.res.RemovalsVerified,
			"retry_safe":        retrySafe,
			"scope":             scope,
			"file":              file,
			"run_id":            runID,
		}
		for k, v := range extra {
			meta[k] = v
		}
		summary := fmt.Sprintf("%s: wrote %d bytes to %s (revision %d, %s profile", tool, out.res.BytesWritten, out.res.AuditPath, out.res.Revision, out.res.Profile)
		if out.res.Idempotent {
			summary += ", already applied — this retry changed nothing"
		}
		summary += ")"
		return mcpToolResult(summary, meta), true
	case out.relay != nil:
		// The host's own §8 envelope (memory_conflict, undeclared_removal,
		// operation_conflict, protected_removal, a refused run …), verbatim:
		// it carries current_revision / current_sha256 for the re-read.
		return memoryMCPToolCallResult{
			IsError: true,
			Content: []memoryMCPToolCallContent{{Type: "text", Text: string(out.relay.body)}},
		}, true
	case out.unknown != "":
		return mcpToolError(tool, "memory_mutation_unknown",
			"the write was sent to the host and its outcome is unknown; nothing was written locally. "+
				"Retry with the SAME operation_id ("+operationID+") and the same content: the ledger answers a retry with the original result",
			map[string]any{"operation_id": operationID, "retryable": true, "sent": true, "reason": out.unknown}), true
	default:
		if memoryGuaranteedRequiredByRuntime() {
			return mcpToolError(tool, "memory_guaranteed_unavailable", out.degradeReason+"; no write was performed", nil), true
		}
		// Provably never sent: the declared legacy fallback applies, on the
		// local dispatcher, exactly as before this bridge existed.
		return memoryMCPToolCallResult{}, false
	}
}

// hostCanonicalReadFull is the whole canonical read, for the model: content,
// the revision anchor, the hash of the bytes on disk, the audit path and the
// provenance of the last confirmed write.
type hostCanonicalReadFull struct {
	Exists         bool            `json:"exists"`
	Content        string          `json:"content"`
	ContentSHA256  string          `json:"content_sha256"`
	Bytes          int             `json:"bytes"`
	Revision       int64           `json:"revision"`
	LedgerRecorded bool            `json:"ledger_recorded"`
	Canonical      bool            `json:"canonical"`
	Drift          bool            `json:"drift"`
	AuditPath      string          `json:"audit_path"`
	Provenance     json.RawMessage `json:"provenance"`
}

func (s *Server) mcpHostRead(r *http.Request, raw json.RawMessage) (memoryMCPToolCallResult, bool) {
	var a mcpHostReadArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return mcpToolError("memory.read", "invalid_args", "invalid args: "+err.Error(), nil), true
	}
	scope, file, ok := mcpHostTarget(a.Tier, a.Key)
	if !ok {
		return memoryMCPToolCallResult{}, false
	}
	canon, out, ok := s.readCanonicalOnHostFull(r, scope, file)
	if !ok {
		if out.relay != nil {
			return memoryMCPToolCallResult{IsError: true,
				Content: []memoryMCPToolCallContent{{Type: "text", Text: string(out.relay.body)}}}, true
		}
		// Unreachable or an older host: the local read is the only one there
		// is, and it says revision 0 / unchecked, which is true of it.
		return memoryMCPToolCallResult{}, false
	}
	meta := map[string]any{
		"exists":           canon.Exists,
		"revision":         canon.Revision,
		"content_sha256":   canon.ContentSHA256,
		"revision_checked": canon.LedgerRecorded,
		"bytes":            canon.Bytes,
		"canonical":        canon.Canonical,
		"drift":            canon.Drift,
		"audit_path":       canon.AuditPath,
		"scope":            scope,
		"file":             file,
	}
	if len(canon.Provenance) > 0 {
		meta["provenance"] = canon.Provenance
	}
	return mcpToolResult(canon.Content, meta), true
}

// readCanonicalOnHostFull is readCanonicalOnHost with the whole response
// decoded, for a caller that wants the content and the provenance rather than
// only the anchor.
func (s *Server) readCanonicalOnHostFull(r *http.Request, scope, file string) (hostCanonicalReadFull, hostMutationOutcome, bool) {
	resp, err := s.memoryHostRequest(r, http.MethodGet, memoryHostCanonicalPath+"?scope="+url.QueryEscape(scope)+"&file="+url.QueryEscape(file), nil)
	if err != nil {
		return hostCanonicalReadFull{}, hostMutationOutcome{degradeReason: "the host canonical read is unreachable: " + err.Error()}, false
	}
	if resp.status == http.StatusNotFound {
		return hostCanonicalReadFull{}, hostMutationOutcome{degradeReason: "this host does not serve " + memoryHostCanonicalPath}, false
	}
	if resp.status != http.StatusOK {
		return hostCanonicalReadFull{}, hostMutationOutcome{relay: &hostMutationRelay{status: resp.status, body: resp.body}}, false
	}
	var canon hostCanonicalReadFull
	if err := json.Unmarshal(resp.body, &canon); err != nil {
		return hostCanonicalReadFull{}, hostMutationOutcome{degradeReason: "the host canonical read could not be decoded: " + err.Error()}, false
	}
	return canon, hostMutationOutcome{}, true
}

// mcpToolResult is a successful tool result: the first content item is what
// the tool always returned (a body, a summary), the second is the metadata
// the model needs for a conditional write, as JSON.
func mcpToolResult(text string, meta map[string]any) memoryMCPToolCallResult {
	encoded, _ := json.Marshal(meta)
	return memoryMCPToolCallResult{
		Content: []memoryMCPToolCallContent{
			{Type: "text", Text: text},
			{Type: "text", Text: string(encoded)},
		},
	}
}

// mcpToolError is a recoverable tool error carrying §8's vocabulary as JSON,
// so the model can branch on error_code rather than on prose.
func mcpToolError(tool, code, message string, extra map[string]any) memoryMCPToolCallResult {
	body := map[string]any{"tool": tool, "error_code": code, "message": message}
	for k, v := range extra {
		body[k] = v
	}
	encoded, _ := json.Marshal(body)
	return memoryMCPToolCallResult{
		IsError: true,
		Content: []memoryMCPToolCallContent{{Type: "text", Text: string(encoded)}},
	}
}
