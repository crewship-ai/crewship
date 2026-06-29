package api

// Internal route for agent-authored skills. An agent writes a complete
// SKILL.md with its own model (no separate generation LLM, so it works on any
// runtime including OAuth-only workspaces) and posts it here. The document is
// validated, injection-scanned, and STAGED under the crew's .proposed
// directory — the same staging the consolidator uses — so it shows up in the
// existing proposed review surface (skill proposed list/approve/reject) for
// free. It never lands in the live registry directly; an operator promotes it.

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/skills"
)

// authorRequest is the body the sidecar posts on behalf of an agent.
type authorRequest struct {
	Content string `json:"content"`
}

// Author stages an agent-authored SKILL.md for human review.
//
// Unlike List/Approve/Reject there is intentionally no MANAGER gate: proposing
// a skill is open to any agent because the staging step is itself the human
// gate (an operator must approve before it ships). The internal-token
// middleware on the route is the trust boundary that keeps this off the public
// API. The crew comes from the sidecar's IPC config (stamped onto the query by
// SkillAuthorAdapter), so an agent cannot author into another crew's namespace.
func (h *SkillProposedHandler) Author(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	crewID := r.URL.Query().Get("crew_id")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id required")
		return
	}

	var body authorRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		replyError(w, http.StatusBadRequest, "content required")
		return
	}

	dir, err := h.proposedDirForCrew(r.Context(), wsID, crewID)
	if err != nil {
		h.mapDirError(w, err)
		return
	}

	staged, err := skills.StageAuthoredSkill(dir, body.Content)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	fileName := filepath.Base(staged.Path)
	if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		CrewID:      crewID,
		Type:        journal.EntryMemorySkillProposed,
		ActorType:   journal.ActorAgent,
		Severity:    journal.SeverityNotice,
		Summary:     "skill authored by agent: " + staged.Slug,
		Payload: map[string]any{
			"slug":        staged.Slug,
			"file_name":   fileName,
			"scan_status": staged.Scan.Status,
			"scan_reason": staged.Scan.Reason,
		},
	}); emitErr != nil {
		h.logger.Warn("skill author emit", "err", emitErr)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"file_name":   fileName,
		"slug":        staged.Slug,
		"scan_status": staged.Scan.Status,
		"scan_reason": staged.Scan.Reason,
	})
}

// SkillAuthorAdapter wraps SkillProposedHandler.Author for the internal sidecar
// route, injecting the workspace from the query (sidecar proxy convention) into
// the context the handler reads. Mirrors SkillInternalAdapter in shape.
type SkillAuthorAdapter struct {
	prop *SkillProposedHandler
}

// NewSkillAuthorAdapter constructs the adapter at router-wiring time so it
// reuses the public SkillProposedHandler instance (shared *sql.DB, journal,
// crew memory root — no duplicate state).
func NewSkillAuthorAdapter(prop *SkillProposedHandler) *SkillAuthorAdapter {
	return &SkillAuthorAdapter{prop: prop}
}

// Author reads workspace_id from the query, injects it into the context the
// Author handler expects, then dispatches. crew_id flows through the query
// untouched.
func (a *SkillAuthorAdapter) Author(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.prop == nil {
		replyError(w, http.StatusInternalServerError, "skill author adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	a.prop.Author(w, r.WithContext(ctx))
}
