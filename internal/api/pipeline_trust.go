package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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

// trustGrants returns the grant store.
//
// The field is assigned once, in NewPipelineHandler, before the handler
// is reachable by any request — so the common path is a plain read. The
// fallback covers the OTHER way this struct is built: a bare literal, as
// a good number of tests do it. That path constructs per call and does
// NOT memoise, deliberately. Caching here would be a write to a field
// concurrent requests also read, which is a data race for the sake of
// saving one struct allocation on a path production never takes.
func (h *PipelineHandler) trustGrants() *pipeline.TrustGrantStore {
	if h.trustGrantStore != nil {
		return h.trustGrantStore
	}
	return pipeline.NewTrustGrantStore(h.db)
}

// emitTrustDecision writes the audit entry for a standing-approval decision.
//
// Shape and contract copied from harbormaster.AfterDecide
// (internal/harbormaster/store_mutate.go): actor type + id, the scope the row
// lives in, refs that join back to the row, and a best-effort emit that never
// fails the request. It runs only AFTER the decision is durable — an entry for
// a grant that did not commit is a lie, and an entry for a revoke that
// affected no row is the same lie in the other direction.
//
// The emitter is nil in tests that build PipelineHandler as a bare literal;
// production wires it via SetJournal (internal/server/server.go). A nil
// emitter drops the entry rather than panicking, exactly as AfterDecide's own
// `if j != nil` does.
func (h *PipelineHandler) emitTrustDecision(ctx context.Context, entryType journal.EntryType, workspaceID, actorID, summary string, payload, refs map[string]any) {
	if h.emitter == nil {
		return
	}
	_, _ = h.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		Type:        entryType,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     summary,
		Payload:     payload,
		Refs:        refs,
	})
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
		// A grant that is already expired can never fire. Storing one
		// would put a row in the audit trail that reads as trust granted
		// while changing nothing, which is worse than refusing it.
		if !exp.After(time.Now()) {
			replyError(w, http.StatusBadRequest, "expires_at is already in the past — that grant could never fire")
			return
		}
		in.ExpiresAt = &exp
	}

	id, err := h.trustGrants().Grant(r.Context(), in)
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
	// The grant is durable now, so the audit entry can be written. Until
	// this existed a standing grant was the only decision in the system with
	// no journal trail — the inbox broadcast below is a UI refresh, not a
	// record, and it survives nothing.
	h.emitTrustDecision(r.Context(), journal.EntryTrustGranted, workspaceID, in.GrantedByUserID,
		fmt.Sprintf("standing approval granted on gate %q of routine %q", body.StepID, slug),
		map[string]any{
			// The hash is the safety property: this grant fires against this
			// body and no other. An audit that omits it cannot say what was
			// trusted, only that something was.
			"definition_hash": p.DefinitionHash,
			"reason":          body.Reason,
			"prior_approvals": body.PriorApprovals,
			"max_uses":        body.MaxUses,
			"expires_at":      body.ExpiresAt,
		},
		map[string]any{
			"trust_grant_id": id,
			"pipeline_id":    p.ID,
			"pipeline_slug":  slug,
			"step_id":        body.StepID,
		})
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

	grants, err := h.trustGrants().List(r.Context(), workspaceID, p.ID)
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
	// The routine in the URL is part of the predicate, not decoration.
	// Grant ids are workspace-unique, so without resolving the slug a
	// request naming routine A would happily retire a grant belonging to
	// routine B.
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
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	if actorID == "" {
		// The store refuses this too; failing here keeps the reason
		// legible instead of surfacing as a 500.
		replyError(w, http.StatusForbidden, "an authenticated user is required to revoke standing approval")
		return
	}
	reason := r.URL.Query().Get("reason")

	// Read the grant BEFORE withdrawing it. Revoke reports only whether a row
	// flipped, and an entry saying "trust withdrawn" without naming the gate
	// is unusable: the audit question is which gate starts asking again, and
	// after the UPDATE the step id would have to be re-read anyway. Losing the
	// race (someone else revoked in between) makes Revoke return false and no
	// entry is written, which is the correct outcome.
	revokedGrant, lookupErr := h.findTrustGrant(r.Context(), workspaceID, p.ID, grantID)
	if lookupErr != nil {
		h.logger.Warn("trust grant: pre-revoke lookup for audit", "error", lookupErr, "grant_id", grantID)
	}

	revoked, err := h.trustGrants().Revoke(r.Context(), workspaceID, p.ID, grantID, actorID, reason)
	if err != nil {
		h.logger.Error("trust grant: revoke", "error", err, "grant_id", grantID)
		replyError(w, http.StatusInternalServerError, "revoke standing approval")
		return
	}
	if !revoked {
		replyError(w, http.StatusNotFound, "no live standing approval with that id on this routine")
		return
	}
	stepID, definitionHash := "", ""
	if revokedGrant != nil {
		stepID, definitionHash = revokedGrant.StepID, revokedGrant.DefinitionHash
	}
	h.emitTrustDecision(r.Context(), journal.EntryTrustRevoked, workspaceID, actorID,
		fmt.Sprintf("standing approval revoked on gate %q of routine %q", stepID, slug),
		map[string]any{
			"definition_hash": definitionHash,
			"reason":          reason,
		},
		map[string]any{
			"trust_grant_id": grantID,
			"pipeline_id":    p.ID,
			"pipeline_slug":  slug,
			"step_id":        stepID,
		})
	h.broadcastInboxUpdated(workspaceID, "trust_revoked")
	writeJSON(w, http.StatusOK, map[string]string{"id": grantID, "status": "revoked"})
}

// findTrustGrant returns one grant by id, scoped to the workspace AND the
// routine — the same predicate Revoke applies, so a lookup can never describe
// a grant the revoke would refuse to touch. Returns (nil, nil) when no such
// grant exists.
//
// The store lists per routine rather than fetching by id; a routine carries a
// handful of gates, so scanning that slice costs less than another query
// method nothing else would call.
func (h *PipelineHandler) findTrustGrant(ctx context.Context, workspaceID, pipelineID, grantID string) (*pipeline.TrustGrant, error) {
	grants, err := h.trustGrants().List(ctx, workspaceID, pipelineID)
	if err != nil {
		return nil, err
	}
	for i := range grants {
		if grants[i].ID == grantID {
			return &grants[i], nil
		}
	}
	return nil, nil
}
