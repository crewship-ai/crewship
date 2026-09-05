package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
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

	wp, ok := h.waitpoints.(waitpointDecideDoor)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	// This endpoint has no workspace JWT — the high-entropy token in the
	// path is the sole auth surface (see doc comment above), so there's no
	// caller-asserted workspace to check the token against. We still thread
	// a workspaceID through CompleteApproval so its UPDATE stays scoped
	// like the authed ApproveWaitpoint path (#1415): look up the token's
	// OWN workspace first and pass that back. This can't reject a
	// legitimate holder of the token (the value always matches itself) —
	// it just keeps both call sites sharing one workspace-scoped query
	// instead of the callback path using a laxer one.
	type workspaceLookup interface {
		WorkspaceIDForToken(ctx context.Context, token string) (string, error)
	}
	workspaceID := ""
	if wl, ok := h.waitpoints.(workspaceLookup); ok {
		if wsID, wErr := wl.WorkspaceIDForToken(r.Context(), token); wErr == nil {
			workspaceID = wsID
		}
	}
	// The decider is the EXTERNAL token holder, attributed to the
	// "external-callback" sentinel: no human user, the token is the
	// authority. Audit queries can distinguish callback completions from
	// inbox approvals by this sentinel. This is not an agent door (B14,
	// #2388): an agent is never handed a waitpoint token — the token
	// travels to the inbox, the pending-waitpoints listing and the CLI, all
	// of them person-facing — so the only way one reaches this route is a
	// person choosing to give it away.
	if err := wp.Decide(r.Context(), workspaceID, token, approved,
		pipeline.WaitpointDecider{Kind: pipeline.DeciderExternal, ID: "external-callback"}, payload); err != nil {
		if !replyWaitpointDecideError(w, h.logger, "waitpoint callback complete", token, err) {
			return
		}
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
