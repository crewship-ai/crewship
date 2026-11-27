package sidecar

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
)

// Native Sheet tools for an ordinary chat run (#5317 D1). They carry no page,
// run, mandate or host URL: the host resolves the one Sheet the human attached
// to the message that started this run, and refuses everything else with a
// stable reason. The operation comes from the tool name, never from the model.

var craftSheetOperations = []string{"read", "acquire", "renew", "apply", "propose", "release"}

func craftSheetToolName(op string) string { return "craft_sheet_" + op }

// craftSheetOnly is said with the read and the apply: the attached workbook is the Sheet, and it
// changes only through these tools.
const craftSheetOnly = " The Sheet is the person's attached workbook: never change a copy of the .xlsx/.xlsm with Python or another tool and never create a new workbook; if a craft_sheet tool refuses, tell the person what it said and stop."

func craftSheetDescription(op string) string {
	return craftSheetDescriptionText(op) + map[string]string{"read": craftSheetOnly, "apply": craftSheetOnly}[op]
}

func craftSheetDescriptionText(op string) string {
	switch op {
	case "read":
		return "Read the Sheet the person attached to this message. Queries: {command:cell.get,params:{cell:A1,sheet?}}, {command:sheet.read,params:{range:A1:D10,sheet?,formulas?}} (at most 10000 cells) {command:document.inspect,params:{}} for the sheet list, and {command:range.columnByHeader,params:{header,sheet?,matchCase?}} for every column under a header text on every sheet ({columns:[{sheet,headerCell,range}]}, range null when nothing is under it). Read never takes the lease. Only a Sheet the person attached to this very message is available; anything else is refused as not_attached."
	case "acquire":
		return "Take the edit lease on the attached Sheet under your own identity before changing it. If the person is editing, it is refused with reason holder_human: do not retry, say so."
	case "renew":
		return "Renew your exact current lease generation. An expired or handed-off lease is refused."
	case "release":
		return "Release your lease generation after checking the save receipt."
	case "propose":
		return "When a direct change is not allowed, prepare a proposal with the same allowlisted commands over the base_version you read, a new request_id and a short summary. The Sheet stays unchanged; report the proposal for the person to review."
	default:
		return "Change the attached Sheet with exact generation, base_version and a new request_id. cell.set params{cell,input,sheet?} and range.setValues params{range,values:[[...]],sheet?} (at most 1000 rows by 200 columns) take numbers, booleans, null and literal text; formulas are not available. range.clear params{range,sheet?} clears the contents of a range. In a Sheet kept as its Excel file, name the sheet in every write with params.sheet (sheet L, cell B8 is 'L'!B8); a write without it is refused as invalid. When a header occurs on several sheets, change each of them and say where. The result names the Sheet (page_id, slug), the new version and the number of changed cells. Only a committed receipt proves it was saved; never retry an uncertain request with a new request_id. Pivot tables are not refreshed by a change: after you change data that a pivot table or a GETPIVOTDATA formula reads, tell the person that the pivot tables are not refreshed and they should refresh them."
	}
}

func craftSheetMCPCatalog() []memoryMCPToolDescriptor {
	tools := make([]memoryMCPToolDescriptor, 0, len(craftSheetOperations))
	for _, op := range craftSheetOperations {
		tools = append(tools, memoryMCPToolDescriptor{Name: craftSheetToolName(op), Description: craftSheetDescription(op), InputSchema: nativeCraftToolSchema(op)})
	}
	return tools
}

// respondCraftSheetMCPToolCall serves one craft_sheet_* call; it reports false
// for any other tool so the caller's chain continues.
func (s *Server) respondCraftSheetMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, args json.RawMessage) bool {
	op, ok := strings.CutPrefix(name, "craft_sheet_")
	if !ok {
		return false
	}
	if s.ipc == nil || !s.ipc.CraftSheetEnabled {
		s.writeRoutinesMCPToolResult(w, req, http.StatusNotFound, mustJSON(map[string]string{"error": "Native Craft agents are not available.", "reason": "feature_off"}))
		return true
	}
	known := false
	for _, candidate := range craftSheetOperations {
		known = known || candidate == op
	}
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !known || !strings.HasPrefix(token, "agtv2.") || !present || !valid {
		s.writeRoutinesMCPToolResult(w, req, http.StatusForbidden, mustJSON(map[string]string{"error": "No Sheet of yours is attached to this message.", "reason": "not_attached"}))
		return true
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	packet, ok := nativeCraftToolPacket("craft_"+op, args)
	if !ok {
		s.writeRoutinesMCPToolResult(w, req, http.StatusBadRequest, mustJSON(map[string]string{"error": "The request is not a valid Sheet operation.", "reason": "invalid_request"}))
		return true
	}
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/craft-native/chat-run", packet, 2<<20, slug)
	if err != nil || response == nil || !json.Valid(response.body) {
		// An uncertain transport result is never Saved and never retried here.
		s.writeRoutinesMCPToolResult(w, req, http.StatusServiceUnavailable, mustJSON(map[string]any{"error": "The Sheet outcome is uncertain; keep the exact original request.", "reason": "engine_failed", "request": json.RawMessage(packet)}))
		return true
	}
	s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	return true
}

// prewarmCraftSheet tells the host, once per run, that the run's tools were
// listed, so the host can open an attached Sheet in the engine while the model
// is still starting. It is a hint: the host answers at once, a run with no
// attached Sheet is refused, and nothing here waits for or depends on it.
func (s *Server) prewarmCraftSheet(r *http.Request) {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		return
	}
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !present || !valid {
		return
	}
	key := sha256.Sum256([]byte(token))
	if _, sent := s.craftSheetPrewarm.LoadOrStore(key, true); sent {
		return
	}
	detached := r.Clone(context.WithoutCancel(r.Context()))
	go func() {
		_, _ = s.sendBoundedHostRequest(detached, http.MethodPost, "/api/v1/internal/craft-native/chat-run", []byte(`{"operation":"prewarm"}`), 4096, slug)
	}()
}
