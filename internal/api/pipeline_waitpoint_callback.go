package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// CompleteWaitpointToken is the PUBLIC waitpoint completion endpoint
// (trigger.dev `wait.forToken` parity). An external system holding a
// waitpoint callback secret completes the wait via an HTTP callback — no
// workspace JWT required: the high-entropy secret in the path is the auth
// surface. The secret is minted on demand by a MANAGER
// (MintWaitpointCallbackToken, shown once) and stored only as a digest; it is
// NOT the waitpoint's id, which every member can see (#1910). The model is
// the same model as the public webhook dispatch endpoint. This lets a
// human-in-the-loop / external-task wait be resolved by a third party
// (approval service, CI job, vendor webhook) instead of only the inbox.
//
// POST /api/v1/waitpoint-tokens/{token}
// Body (optional): { "approved": true, "payload": <any JSON> }
//   - approved defaults to true (the common "task done → continue" case)
//   - payload is stored on the waitpoint for the resumed step to read
//
// This default is the OPPOSITE of the authed sibling, ApproveWaitpoint
// (pipelines_exec.go): that endpoint defaults an omitted `approved` to
// false (deny). Both are intentional for their own caller population —
// this route has no JWT (the token is the sole credential) and serves
// external systems whose completion signal is often a bare POST with
// no body, so it fails open; the authed route serves a JWT-holding
// human/system making a considered decision, so it fails closed. See
// docs/api-reference/workspaces.mdx "Defaults differ from the public
// callback" for the full reasoning. Do not change one without the
// other's rationale in mind.
func (h *PipelineHandler) CompleteWaitpointToken(w http.ResponseWriter, r *http.Request) {
	if h.waitpoints == nil {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store not wired")
		return
	}
	secret := r.PathValue("token")
	if secret == "" {
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

	store, ok := h.waitpoints.(interface {
		DecideCallback(context.Context, string, bool, string) (string, string, error)
	})
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	id, workspaceID, err := store.DecideCallback(r.Context(), secret, approved, payload)
	if err != nil {
		if id != "" {
			err = explainDecideError(r.Context(), store, workspaceID, id, err)
		}
		replyWaitpointDecideError(w, h.logger, "waitpoint callback complete", id, err)
		return
	}

	// Resume the parked run — same path as the authed approve handler.
	type runLookup interface {
		RunIDForToken(ctx context.Context, token string) (string, error)
	}
	if lk, ok := h.waitpoints.(runLookup); ok {
		if runID, lerr := lk.RunIDForToken(r.Context(), id); lerr == nil && runID != "" {
			h.newExecutor().ResumeAfterApproval(runID, h.logger)
		} else if lerr != nil {
			h.logger.Warn("waitpoint callback resume: run lookup failed", "error", lerr, "waitpoint", id)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": approved})
}

// waitpointCallbackMinter mints a waitpoint's callback secret.
type waitpointCallbackMinter interface {
	MintCallbackToken(ctx context.Context, workspaceID, id, actorID string) (secret string, rotated bool, err error)
}

// MintWaitpointCallbackToken mints the public callback URL for one pending
// waitpoint and returns it ONCE. POST
// /api/v1/workspaces/{ws}/pipelines/waitpoints/{token}/callback-token
//
// The secret in the URL is stored only as a digest (#1910), so it cannot be
// listed or read back later; a lost URL is replaced by minting again, which
// also revokes the previous one (`rotated: true`). Minting is a
// capability grant that can release a gated run without authentication, so
// it takes the approve route's own tier (MANAGER+) and is audited.
func (h *PipelineHandler) MintWaitpointCallbackToken(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "update") {
		return
	}
	minter, ok := h.waitpoints.(waitpointCallbackMinter)
	if h.waitpoints == nil || !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store not wired")
		return
	}
	id := r.PathValue("token")
	if id == "" {
		replyError(w, http.StatusBadRequest, "token required")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	actor := UserFromContext(r.Context())
	if actor == nil || actor.ID == "" {
		replyError(w, http.StatusForbidden, "authenticated manager required")
		return
	}
	secret, rotated, err := minter.MintCallbackToken(r.Context(), workspaceID, id, actor.ID)
	switch {
	case errors.Is(err, pipeline.ErrWaitpointNotFound):
		replyError(w, http.StatusNotFound, "waitpoint not found")
		return
	case errors.Is(err, pipeline.ErrCallbackNotOffered):
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case errors.Is(err, pipeline.ErrAlreadyDecided):
		replyError(w, http.StatusConflict, pipeline.ErrAlreadyDecided.Error())
		return
	case err != nil:
		h.logger.Error("waitpoint callback token mint", "error", err, "waitpoint", pipeline.PublicWaitpointID(workspaceID, id))
		replyError(w, http.StatusInternalServerError, "Failed to mint callback token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":        pipeline.PublicWaitpointID(workspaceID, id),
		"callback_url": InstanceURLFromRequest(r, "") + "/api/v1/waitpoint-tokens/" + secret,
		"rotated":      rotated,
	})
}
