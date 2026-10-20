package api

// Restore transport adapted from #3268/22b081c8. The existing service and Store
// retain all schema decoding, writer fencing, generation and receipt semantics.
import (
	"encoding/json"
	"io"
	"net/http"
)

type documentRestoreInput struct {
	PageID     string `json:"page_id"`
	ID         string `json:"id"`
	Schema     int    `json:"schema"`
	Epoch      string `json:"epoch"`
	IfRev      int64  `json:"ifRev"`
	Checkpoint string `json:"checkpoint"`
}

type documentRestoreResponse struct {
	PageID    string `json:"page_id"`
	ID        string `json:"id"`
	Rev       int64  `json:"rev"`
	Epoch     string `json:"epoch"`
	Kind      string `json:"kind"`
	Schema    int    `json:"schema,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

func (h *PageHandler) RestoreDocumentCheckpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	fields, shapeErr := documentDraftObject(raw)
	var input documentRestoreInput
	if err != nil || shapeErr != nil || len(fields) != 6 || json.Unmarshal(raw, &input) != nil ||
		!documentCheckpointID.MatchString(input.PageID) || !documentCheckpointID.MatchString(input.ID) || !documentCheckpointID.MatchString(input.Checkpoint) ||
		(input.Schema != 2 && input.Schema != 3) || input.IfRev < 0 || input.IfRev >= 9007199254740991 || input.Epoch == "" || len(input.Epoch) > 256 {
		replyError(w, 400, "original bounded Document restore packet required")
		return
	}
	for _, key := range []string{"page_id", "id", "schema", "epoch", "ifRev", "checkpoint"} {
		if fields[key] == nil {
			replyError(w, 400, "closed Document restore packet required")
			return
		}
	}
	rec, ok := h.documentPage(w, r)
	if !ok {
		return
	}
	if rec.ID != input.PageID {
		replyError(w, 400, "original Page identity required")
		return
	}
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	result, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), "Bearer "+extractToken(r), rec.ID, "restore", json.RawMessage(raw))
	if err != nil || status < 200 || status >= 300 {
		if err == nil && (status == 400 || status == 401 || status == 403 || status == 404 || status == 409 || status == 413 || status == 422 || status == 429) {
			replyError(w, status, "Document restore refused; confirmed content preserved")
		} else {
			replyError(w, 503, "Document restore confirmation unavailable; retry the original packet")
		}
		return
	}
	var receipt documentRestoreResponse
	if json.Unmarshal(result, &receipt) != nil || receipt.ID != input.ID || receipt.Rev != input.IfRev+1 || receipt.Kind != "restore" || receipt.Epoch == "" || receipt.Epoch == input.Epoch || len(receipt.Epoch) > 256 {
		replyError(w, 502, "Document restore confirmation unavailable; retry the original packet")
		return
	}
	// First-commit responses omit schema; replay may include the pinned schema.
	// Do not invent a schema or expose internal snapshot/lease/actor fields.
	if receipt.Schema != 0 && (receipt.Schema < 1 || receipt.Schema > 3) {
		replyError(w, 409, "unsupported restored Document schema")
		return
	}
	receipt.PageID = rec.ID
	writeJSON(w, 200, receipt)
}
