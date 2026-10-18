package api

import (
	"encoding/json"
	"net/http"
	"regexp"
)

var documentCheckpointID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type documentCheckpointSummary struct {
	ID        string `json:"id"`
	Rev       int64  `json:"rev"`
	Schema    int    `json:"schema"`
	Actor     string `json:"actor"`
	CreatedAt string `json:"created_at"`
}

func (h *PageHandler) ListDocumentCheckpoints(w http.ResponseWriter, r *http.Request) {
	h.serveDocumentHistory(w, r, false)
}

func (h *PageHandler) ReadDocumentCheckpoint(w http.ResponseWriter, r *http.Request) {
	h.serveDocumentHistory(w, r, true)
}

// Only the private engine decodes retained snapshots. This human read boundary
// forwards the original credential for live, transactional authorization and
// publishes a closed projection without binary snapshots or writer authority.
func (h *PageHandler) serveDocumentHistory(w http.ResponseWriter, r *http.Request, selected bool) {
	w.Header().Set("Cache-Control", "no-store")
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	id := r.PathValue("checkpoint")
	if selected && !documentCheckpointID.MatchString(id) {
		replyError(w, 400, "bounded checkpoint identity required")
		return
	}
	rec, ok := h.documentPage(w, r)
	if !ok || !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	operation := "checkpoints"
	body := map[string]string{}
	if selected {
		operation = "checkpoint-read"
		body["checkpoint"] = id
	}
	raw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), "Bearer "+extractToken(r), rec.ID, operation, body)
	if err != nil || status < 200 || status >= 300 {
		if err == nil && (status == 401 || status == 403 || status == 404 || status == 409 || status == 429) {
			replyError(w, status, "document history unavailable")
		} else {
			replyError(w, 503, "document history unavailable")
		}
		return
	}
	if !selected {
		var value struct {
			Checkpoints []documentCheckpointSummary `json:"checkpoints"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Checkpoints == nil || len(value.Checkpoints) > 1000 {
			replyError(w, 502, "invalid document history response")
			return
		}
		seen := make(map[string]bool, len(value.Checkpoints))
		for _, checkpoint := range value.Checkpoints {
			if !documentCheckpointID.MatchString(checkpoint.ID) || seen[checkpoint.ID] || checkpoint.Rev < 0 || checkpoint.Rev > 9007199254740991 || checkpoint.Schema < 1 {
				replyError(w, 502, "invalid document history response")
				return
			}
			seen[checkpoint.ID] = true
		}
		writeJSON(w, 200, value)
		return
	}
	var value struct {
		ID      string          `json:"id"`
		Rev     int64           `json:"rev"`
		Schema  int             `json:"schema"`
		Content json.RawMessage `json:"content"`
	}
	var root struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &value) != nil || value.ID != id || value.Rev < 0 || value.Rev > 9007199254740991 || json.Unmarshal(value.Content, &root) != nil || root.Type != "doc" {
		replyError(w, 502, "invalid document checkpoint response")
		return
	}
	if value.Schema < 1 || value.Schema > 3 {
		replyError(w, 409, "unsupported document checkpoint schema")
		return
	}
	writeJSON(w, 200, map[string]any{"page_id": rec.ID, "id": value.ID, "rev": value.Rev, "schema": value.Schema, "content": value.Content})
}
