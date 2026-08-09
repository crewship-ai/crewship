package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Standing approval grants — the "you have approved this same gate ten
// times, stop asking me" surface.
//
// These handlers are deliberately thin. The decision they record is
// consulted at exactly one place in the runtime
// (SQLWaitpointStore.CreateApproval), and its safety comes from the
// definition_hash the grant is pinned to rather than from anything
// enforced here. What this layer owes the operator is that the hash
// pinned is the one they were actually looking at.

// trustGrantRequest is the body of POST .../pipelines/{slug}/trust.
type trustGrantRequest struct {
	StepID string `json:"step_id"`
	// DefinitionHash, when present, must match the routine's current
	// definition. It is the anti-TOCTOU token: the inbox card that
	// offered the grant carries the hash it was rendered for, and a
	// routine edited in between must invalidate the offer rather than
	// silently retarget it.
	DefinitionHash string `json:"definition_hash,omitempty"`
	Reason         string `json:"reason,omitempty"`
	PriorApprovals int    `json:"prior_approvals,omitempty"`
	MaxUses        *int   `json:"max_uses,omitempty"`
	// ExpiresAt is RFC3339. Absent = no expiry.
	ExpiresAt string `json:"expires_at,omitempty"`
}

// trustGrantView is a grant as rendered to clients. Live is computed
// server-side so the CLI and the dashboard cannot disagree about whether
// a grant is still in force.
type trustGrantView struct {
	pipeline.TrustGrant
	Live bool `json:"live"`
}

// GrantTrust records a standing approval for one gate of one routine.
// MANAGER+ — disarming a gate is the same class of act as approving the
// routine that contains it.
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/trust
func (h *PipelineHandler) GrantTrust(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to grant standing approval")
		return
	}
	var body trustGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.StepID == "" {
		// Without a step this would be trust in the routine as a whole,
		// which is a different (and much blunter) thing than what this
		// endpoint offers.
		replyError(w, http.StatusBadRequest, "step_id is required — trust is granted per gate, not per routine")
		return
	}

	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("trust grant: load routine", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if body.DefinitionHash != "" && body.DefinitionHash != p.DefinitionHash {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":           "routine changed since the approval was offered — review it again before trusting this gate",
			"definition_hash": p.DefinitionHash,
		})
		return
	}

	in := pipeline.GrantInput{
		WorkspaceID:    workspaceID,
		PipelineID:     p.ID,
		StepID:         body.StepID,
		DefinitionHash: p.DefinitionHash,
		Reason:         body.Reason,
		PriorApprovals: body.PriorApprovals,
		MaxUses:        body.MaxUses,
	}
	if user := UserFromContext(r.Context()); user != nil {
		in.GrantedByUserID = user.ID
	}
	if in.GrantedByUserID == "" {
		// A standing grant with no author is an unattributable decision
		// to remove a human from the loop.
		replyError(w, http.StatusForbidden, "an authenticated user is required to grant standing approval")
		return
	}
	if body.ExpiresAt != "" {
		exp, perr := time.Parse(time.RFC3339, body.ExpiresAt)
		if perr != nil {
			replyError(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return
		}
		in.ExpiresAt = &exp
	}

	grants := pipeline.NewTrustGrantStore(h.db)
	id, err := grants.Grant(r.Context(), in)
	if errors.Is(err, pipeline.ErrGrantExists) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this gate is already trusted for the current definition",
		})
		return
	}
	if err != nil {
		h.logger.Error("trust grant: insert", "error", err, "slug", slug, "step_id", body.StepID)
		replyError(w, http.StatusInternalServerError, "grant standing approval")
		return
	}
	h.broadcastInboxUpdated(workspaceID, "trust_granted")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              id,
		"slug":            slug,
		"step_id":         body.StepID,
		"definition_hash": p.DefinitionHash,
	})
}

// ListTrustGrants returns every grant recorded against a routine,
// revoked ones included — the audit question is "who trusted this gate,
// and who took it back", which a live-only list cannot answer.
//
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/trust
func (h *PipelineHandler) ListTrustGrants(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("trust grants: load routine", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}

	grants, err := pipeline.NewTrustGrantStore(h.db).List(r.Context(), workspaceID, p.ID)
	if err != nil {
		h.logger.Error("trust grants: list", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "list standing approvals")
		return
	}
	now := time.Now()
	views := make([]trustGrantView, 0, len(grants))
	for _, g := range grants {
		views = append(views, trustGrantView{TrustGrant: g, Live: g.Live(now)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":   slug,
		"grants": views,
		// The hash a client would have to match to grant new trust. Saves
		// the CLI a second round-trip.
		"definition_hash": p.DefinitionHash,
	})
}

// RevokeTrust withdraws a standing grant. The row is kept and marked, so
// the trail survives the withdrawal. MANAGER+.
//
// DELETE /api/v1/workspaces/{ws}/pipelines/{slug}/trust/{grantId}
func (h *PipelineHandler) RevokeTrust(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to revoke standing approval")
		return
	}
	grantID := r.PathValue("grantId")
	if grantID == "" {
		replyError(w, http.StatusBadRequest, "grant id is required")
		return
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	reason := r.URL.Query().Get("reason")

	revoked, err := pipeline.NewTrustGrantStore(h.db).Revoke(r.Context(), workspaceID, grantID, actorID, reason)
	if err != nil {
		h.logger.Error("trust grant: revoke", "error", err, "grant_id", grantID)
		replyError(w, http.StatusInternalServerError, "revoke standing approval")
		return
	}
	if !revoked {
		replyError(w, http.StatusNotFound, "no live standing approval with that id")
		return
	}
	h.broadcastInboxUpdated(workspaceID, "trust_revoked")
	writeJSON(w, http.StatusOK, map[string]string{"id": grantID, "status": "revoked"})
}
