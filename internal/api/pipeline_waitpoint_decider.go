package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// waitpointDecideDoor is the resolve door every waitpoint-completing
// handler goes through (B14, #2388). *pipeline.SQLWaitpointStore
// implements it; the WaitpointStore interface deliberately does not
// carry it, so handlers type-assert and answer 503 when the wired store
// cannot decide.
type waitpointDecideDoor interface {
	Decide(ctx context.Context, workspaceID, token string, approved bool, decider pipeline.WaitpointDecider, payload string) error
}

// waitpointDeciderFromRequest derives WHO is deciding from the request's
// auth context — never from anything the caller wrote in the body.
//
//   - A session or CLI-token principal (RequireAuth) is a person: the
//     inbox, the routine page, `crewship routine waitpoints approve`.
//   - A crew- or workspace-bound X-Internal-Token (requireInternal) is an
//     agent's sidecar. No agent-facing route reaches the door today —
//     authed routes reject internal tokens with 401 — but the door checks
//     the kind anyway, so wiring one later cannot quietly become a way for
//     a peer agent to say "GO" (PRD §18 scenario 10).
//   - Anything else has no principal and is refused by the door: an empty
//     Kind fails closed.
func waitpointDeciderFromRequest(r *http.Request) pipeline.WaitpointDecider {
	ctx := r.Context()
	if u := UserFromContext(ctx); u != nil {
		return pipeline.WaitpointDecider{Kind: pipeline.DeciderUser, ID: u.ID}
	}
	if crew := InternalTokenCrewFromContext(ctx); crew != "" {
		return pipeline.WaitpointDecider{Kind: pipeline.DeciderAgent, ID: "crew:" + crew}
	}
	if ws := InternalTokenWorkspaceFromContext(ctx); ws != "" {
		return pipeline.WaitpointDecider{Kind: pipeline.DeciderAgent, ID: "workspace-token:" + ws}
	}
	return pipeline.WaitpointDecider{}
}

// replyWaitpointDecideError maps a Decide error onto the wire. A refused
// decider is 403 with the reason in the body — the waitpoint is untouched
// and the attempt is on the audit log; an already-decided or
// foreign-workspace token is 409, unchanged from before; anything else is
// a 500.
func replyWaitpointDecideError(w http.ResponseWriter, logger *slog.Logger, op, token string, err error) {
	switch {
	case errors.Is(err, pipeline.ErrDeciderNotAllowed):
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":  err.Error(),
			"reason": "waitpoint_decider_not_allowed",
		})
	case errors.Is(err, pipeline.ErrAlreadyDecided), err.Error() == pipeline.ErrAlreadyDecided.Error():
		replyError(w, http.StatusConflict, pipeline.ErrAlreadyDecided.Error())
	default:
		logger.Error(op, "error", err, "token", tokenFingerprint(token))
		replyError(w, http.StatusInternalServerError, "Failed to complete waitpoint")
	}
}
