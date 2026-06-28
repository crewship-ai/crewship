package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// CompleteWaitpointToken is the PUBLIC waitpoint completion endpoint
// (trigger.dev `wait.forToken` parity). An external system holding a
// waitpoint token completes the wait via an HTTP callback — no workspace
// JWT required: the high-entropy token in the path is the auth surface,
// the same model as the public webhook dispatch endpoint. This lets a
// human-in-the-loop / external-task wait be resolved by a third party
// (approval service, CI job, vendor webhook) instead of only the inbox.
//
// POST /api/v1/waitpoint-tokens/{token}
// Body (optional): { "approved": true, "payload": <any JSON> }
//   - approved defaults to true (the common "task done → continue" case)
//   - payload is stored on the waitpoint for the resumed step to read
func (h *PipelineHandler) CompleteWaitpointToken(w http.ResponseWriter, r *http.Request) {
	if h.waitpoints == nil {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store not wired")
		return
	}
	token := r.PathValue("token")
	if token == "" {
		replyError(w, http.StatusBadRequest, "token required")
		return
	}

	// approved defaults to true: a bare POST means "the external task
	// finished, continue the run". Callers can deny with {"approved":false}.
	body := struct {
		Approved *bool           `json:"approved"`
		Payload  json.RawMessage `json:"payload"`
	}{}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	approved := true
	if body.Approved != nil {
		approved = *body.Approved
	}
	payload := ""
	if len(body.Payload) > 0 {
		payload = string(body.Payload)
	}

	type approver interface {
		CompleteApproval(ctx context.Context, token string, approved bool, deciderUserID, payload string) error
	}
	wp, ok := h.waitpoints.(approver)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	// deciderUserID = "external-callback": no human user, the token is the
	// authority. Audit queries can distinguish callback completions from
	// inbox approvals by this sentinel.
	if err := wp.CompleteApproval(r.Context(), token, approved, "external-callback", payload); err != nil {
		if err.Error() == "waitpoint: already decided or expired" {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		h.logger.Error("waitpoint callback complete", "error", err, "token", tokenFingerprint(token))
		replyError(w, http.StatusInternalServerError, "Failed to complete waitpoint")
		return
	}

	// Resume the parked run — same path as the authed approve handler.
	type runLookup interface {
		RunIDForToken(ctx context.Context, token string) (string, error)
	}
	if lk, ok := h.waitpoints.(runLookup); ok {
		if runID, lerr := lk.RunIDForToken(r.Context(), token); lerr == nil && runID != "" {
			h.newExecutor().ResumeAfterApproval(runID, h.logger)
		} else if lerr != nil {
			h.logger.Warn("waitpoint callback resume: run lookup failed", "error", lerr, "token", tokenFingerprint(token))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": approved})
}
