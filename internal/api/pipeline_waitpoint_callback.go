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

	wp, ok := h.waitpoints.(waitpointDecideDoor)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	// The path carries the CALLBACK SECRET, not the waitpoint id (#1910).
	// It is hashed and the digest looked up; the result is the waitpoint's
	// public id and its own workspace, which are what the decide door and
	// the resume path key on. The workspace is the row's own, so this
	// cannot reject a legitimate holder — it keeps this call site on the
	// same workspace-scoped UPDATE as the authed path (#1415).
	//
	// Anything that is not a live secret — the public id, a stored digest,
	// a waitpoint nobody minted a URL for, garbage — resolves to nothing
	// and gets the same 409 an unknown token always got, so the endpoint
	// does not say which of those it was.
	resolver, ok := h.waitpoints.(waitpointCallbackResolver)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	id, workspaceID, err := resolver.ResolveCallbackToken(r.Context(), secret)
	if err != nil {
		h.logger.Error("waitpoint callback: resolve", "error", err, "token", tokenFingerprint(secret))
		replyError(w, http.StatusInternalServerError, "Failed to complete waitpoint")
		return
	}
	if id == "" {
		replyError(w, http.StatusConflict, pipeline.ErrAlreadyDecided.Error())
		return
	}
	// The decider is the EXTERNAL secret holder, attributed to the
	// "external-callback" sentinel: no human user, the secret is the
	// authority. Audit queries can distinguish callback completions from
	// inbox approvals by this sentinel. This is not an agent door (B14,
	// #2388): an agent is never handed a callback secret — only a MANAGER
	// can mint one, and it is shown to them once — so the only way one
	// reaches this route is a person choosing to give it away.
	if err := wp.Decide(r.Context(), workspaceID, id, approved,
		pipeline.WaitpointDecider{Kind: pipeline.DeciderExternal, ID: "external-callback"}, payload); err != nil {
		err = explainDecideError(r.Context(), wp, workspaceID, id, err)
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

// waitpointCallbackResolver maps a presented callback secret to the waitpoint
// it authorises. *pipeline.SQLWaitpointStore implements it.
type waitpointCallbackResolver interface {
	ResolveCallbackToken(ctx context.Context, presented string) (id, workspaceID string, err error)
}

// waitpointCallbackMinter mints a waitpoint's callback secret.
type waitpointCallbackMinter interface {
	MintCallbackToken(ctx context.Context, workspaceID, id string) (secret string, rotated bool, err error)
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
	secret, rotated, err := minter.MintCallbackToken(r.Context(), workspaceID, id)
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
		h.logger.Error("waitpoint callback token mint", "error", err, "waitpoint", id)
		replyError(w, http.StatusInternalServerError, "Failed to mint callback token")
		return
	}
	auditFromRequest(r, h.db, "waitpoint.callback_token.minted", "waitpoint", id, map[string]interface{}{"rotated": rotated})
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":        id,
		"callback_url": InstanceURLFromRequest(r, "") + "/api/v1/waitpoint-tokens/" + secret,
		"rotated":      rotated,
	})
}
