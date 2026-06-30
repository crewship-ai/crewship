package api

// Routine governance: the maker-checker gate over routine authoring plus the
// admin disable/enable airbag. Mirrors the SKILLS proposed-review flow
// (skills_proposed_handler.go): risky agent/user-authored routines land as
// status='proposed' with a MANAGER+ inbox item; approve flips them live and
// resolves the item; an OWNER/ADMIN can disable a live routine (cancelling
// any in-flight runs) and re-enable it later.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routineProposalInboxSource is the (kind, source_id) dedup key tying a
// proposed routine to its inbox review item. The save path inserts it;
// approve/reject resolve it. Keyed on workspace + slug so re-proposing the
// same routine is idempotent (INSERT OR IGNORE on this key) and approve/reject
// can resolve it without re-reading the row.
func routineProposalInboxSource(workspaceID, slug string) string {
	return "routineprop:" + workspaceID + ":" + slug
}

// classifyRoutineRisk decides whether a routine save must land as 'proposed'
// (human review) or may go live as 'active'. Risky when the DSL declares any
// http/egress step, any code-runtime step, any credentials_required, OR an
// integrations_required the author crew can't currently satisfy. Returns the
// risk reasons for the audit / inbox payload.
//
// The integration factor reuses the W0 resolver (resolveCrewIntegrations) and
// is FAIL-OPEN exactly like the run gate: if there's no author crew, no DB, or
// the resolver errors, we don't treat the integration as a risk factor (a
// resolver hiccup must not wedge every save into the review queue). The static
// factors still apply.
func (h *PipelineHandler) classifyRoutineRisk(ctx context.Context, workspaceID, crewID string, dsl *pipeline.DSL) (bool, []string) {
	reasons := dsl.StaticRiskReasons()
	reasons = append(reasons, h.unmetIntegrationReasons(ctx, workspaceID, crewID, dsl)...)
	return len(reasons) > 0, reasons
}

// unmetIntegrationReasons returns a RiskUnmetIntegration:<slug> reason for each
// declared integration the author crew hasn't connected. Fail-open on missing
// crew/db/resolver-error and under the default-connector wildcard.
func (h *PipelineHandler) unmetIntegrationReasons(ctx context.Context, workspaceID, crewID string, dsl *pipeline.DSL) []string {
	required := dsl.NormalizedIntegrationsRequired()
	if len(required) == 0 || h.db == nil || crewID == "" {
		return nil
	}
	available, err := resolveCrewIntegrations(ctx, h.db, workspaceID, crewID)
	if err != nil {
		h.logger.Warn("routine risk: integration resolve failed, treating as satisfiable (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID, "error", err)
		return nil
	}
	if available[crewIntegrationsWildcard] {
		return nil
	}
	var out []string
	for _, want := range required {
		if !available[want] {
			out = append(out, pipeline.RiskUnmetIntegration+":"+want)
		}
	}
	return out
}

// statusForRisk maps the risk verdict onto a persisted status.
func statusForRisk(risky bool) string {
	if risky {
		return "proposed"
	}
	return "active"
}

// proposeRoutineInbox raises the MANAGER+ inbox review item for a routine that
// landed as 'proposed', then nudges the workspace to refresh its inbox. Mirrors
// the skills author flow: KindEscalation, blocking, high priority. Best-effort
// — a projection failure must not fail the save (the proposed row is
// authoritative).
func (h *PipelineHandler) proposeRoutineInbox(ctx context.Context, workspaceID string, saved *pipeline.Pipeline, reasons []string, senderName string) {
	_ = inbox.Insert(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    routineProposalInboxSource(workspaceID, saved.Slug),
		TargetRole:  "MANAGER",
		Title:       "Routine proposed for review: " + saved.Slug,
		BodyMD: "A routine was authored that needs approval before it can run (reasons: " +
			strings.Join(reasons, ", ") + "). Approve it to activate the routine, or reject it.",
		SenderType: "pipeline",
		SenderName: senderName,
		Priority:   "high",
		Blocking:   true,
		Payload: map[string]interface{}{
			"kind":           "routine_proposal",
			"slug":           saved.Slug,
			"pipeline_id":    saved.ID,
			"author_crew_id": saved.AuthorCrewID,
			"risk_reasons":   reasons,
		},
	})
	h.broadcastInboxUpdated(workspaceID, "routine_proposed")
}

// broadcastInboxUpdated pushes the same inbox.updated event the inbox handler
// uses so any subscribed client repaints its inbox. No-op when the WS
// broadcaster isn't wired (tests / headless boot).
func (h *PipelineHandler) broadcastInboxUpdated(workspaceID, reason string) {
	if h.ws == nil {
		return
	}
	h.ws.BroadcastWorkspace(workspaceID, "inbox.updated", map[string]string{"reason": reason})
}

// gateRoutineStatus blocks a run whose routine isn't 'active'. Returns true
// (having written the response) when the run must be refused. Placed alongside
// the W0 integration gate in Run. dry_run is intentionally NOT gated (preview
// is always allowed); test_run executes an unsaved draft so has no status.
func (h *PipelineHandler) gateRoutineStatus(w http.ResponseWriter, p *pipeline.Pipeline) bool {
	switch p.Status {
	case "", "active":
		return false
	case "proposed":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is awaiting approval",
			"status": "proposed",
			"hint":   "a MANAGER must approve this routine before it can run",
		})
		return true
	case "disabled":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is disabled",
			"status": "disabled",
			"hint":   "an OWNER or ADMIN must re-enable this routine before it can run",
		})
		return true
	default:
		// Unknown status — fail closed so a future state can't silently run.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is not active",
			"status": p.Status,
		})
		return true
	}
}

// Approve flips a proposed routine to active and resolves its inbox review
// item. MANAGER+ (canRole "create" — same threshold as save/import).
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/approve
func (h *PipelineHandler) Approve(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to approve routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine approve: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "active"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine approve: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "approve routine")
		return
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	inbox.ResolveBySource(r.Context(), h.db, h.logger,
		inbox.KindEscalation, routineProposalInboxSource(workspaceID, slug), "approved", actorID)
	h.broadcastInboxUpdated(workspaceID, "routine_approved")
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "active"})
}

// Reject removes a proposed routine (soft-delete, mirroring the skills reject
// which deletes the staged file) and resolves its inbox item. MANAGER+.
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/reject
func (h *PipelineHandler) Reject(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to reject routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine reject: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SoftDelete(r.Context(), p.ID); err != nil && !errors.Is(err, pipeline.ErrNotFound) {
		h.logger.Error("routine reject: soft delete", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "reject routine")
		return
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	inbox.ResolveBySource(r.Context(), h.db, h.logger,
		inbox.KindEscalation, routineProposalInboxSource(workspaceID, slug), "rejected", actorID)
	h.broadcastInboxUpdated(workspaceID, "routine_rejected")
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "rejected"})
}

// Disable is the admin airbag: flip a routine to 'disabled' and cancel any
// in-flight runs of it. OWNER/ADMIN only (canRole "manage" — same threshold as
// cancel/rollback).
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/disable
func (h *PipelineHandler) Disable(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "OWNER/ADMIN role required to disable routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine disable: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "disabled"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine disable: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "disable routine")
		return
	}
	// Cancel in-flight runs of this routine so disable takes effect
	// immediately, not just for future triggers. Best-effort — a run that
	// finishes between Active() and Cancel() simply isn't found.
	cancelled := 0
	if h.runs != nil {
		for _, info := range h.runs.Active(workspaceID) {
			if info.PipelineID == p.ID || info.PipelineSlug == slug {
				if cerr := h.runs.Cancel(info.RunID); cerr == nil {
					cancelled++
				}
			}
		}
	}
	h.broadcastInboxUpdated(workspaceID, "routine_disabled")
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":           slug,
		"status":         "disabled",
		"cancelled_runs": cancelled,
	})
}

// Enable lifts a disable, returning the routine to 'active'. OWNER/ADMIN only.
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/enable
func (h *PipelineHandler) Enable(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "OWNER/ADMIN role required to enable routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine enable: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "active"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine enable: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "enable routine")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "active"})
}
