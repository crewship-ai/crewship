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

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/skills"
)

// authorRequest is the body the sidecar posts on behalf of an agent.
type authorRequest struct {
	Content string `json:"content"`
}

// skillProposalInboxSource is the (kind, source_id) dedup key tying a staged
// skill to its inbox review item. Author inserts it; Approve/Reject resolve it.
// Re-authoring the same crew+file is idempotent (INSERT OR IGNORE on this key).
func skillProposalInboxSource(crewID, fileName string) string {
	return "skillprop:" + crewID + ":" + fileName
}

// Author stages an agent-authored SKILL.md for human review.
//
// Unlike List/Approve/Reject there is intentionally no MANAGER gate: proposing
// a skill is open to any agent because the staging step is itself the human
// gate (an operator must approve before it ships). The internal-token
// middleware on the route is the trust boundary that keeps this off the public
// API. The crew comes from the sidecar's IPC config (stamped onto the query by
// SkillAuthorAdapter), so an agent cannot author into another crew's namespace.
//
// #1768 — the policy.ActionSkillCreate gate runs here too, but this route was
// the one entry in sidecarRoutesAwaitingPolicyGate that was ALREADY inert:
// staging plus a blocking, ADMIN-addressed inbox item is exactly the
// "created but cannot act until an operator approves" shape the other five
// routes had to be given. So the gate adds the DecisionRejected arm (which
// the matrix never returns for skill_create today, but would if the matrix
// changes) and stamps the decision onto the journal payload; the staging
// behaviour itself is unchanged at every level.
//
// It is deliberately NOT relaxed at full autonomy, where the matrix says
// AutoLogInbox (proceed with a non-blocking notice). Proceeding here would
// mean promoting the skill into the live registry from an agent call, and
// this route has no promotion path — `skills proposed approve` does. Adding
// one would be granting a new capability under the banner of adding a gate,
// so the route stays at its stricter behaviour and the decision is recorded.
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

	gate, ok := gateInternalAction(w, r, h.policyResolver, h.logger, crewID,
		policy.ActionSkillCreate, "Skill authoring")
	if !ok {
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
			"slug":           staged.Slug,
			"file_name":      fileName,
			"scan_status":    staged.Scan.Status,
			"scan_reason":    staged.Scan.Reason,
			"decision":       string(gate.Decision),
			"autonomy_level": string(gate.Level),
			"policy_action":  string(gate.Action),
		},
	}); emitErr != nil {
		h.logger.Warn("skill author emit", "err", emitErr)
	}

	// Surface the proposal in the inbox so a manager reviews/approves it in
	// the UI (not just via the CLI). Visible to MANAGER+; blocking because it
	// needs an explicit decision. The payload carries everything the inbox card
	// needs to call proposed approve/reject. Fire-and-forget: a projection
	// failure must not fail the author call (the staged file is authoritative).
	_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
		WorkspaceID: wsID,
		Kind:        inbox.KindEscalation,
		SourceID:    skillProposalInboxSource(crewID, fileName),
		// ADMIN, not MANAGER: /skills/proposed/approve is roleManage, so a
		// MANAGER-addressed row could only ever hand its reader a 403. Address
		// what can act — the visibility clause is hierarchical, so OWNER sees
		// it too. See TestInboxTargetRoleMatchesDecider.
		TargetRole: "ADMIN",
		Title:      "Skill proposed for review: " + staged.Slug,
		BodyMD:     "An agent authored a new skill. Approve it to add it to the crew, or reject it.",
		SenderType: "agent",
		SenderName: "Agent skill author",
		Priority:   "high",
		Blocking:   true,
		Payload: map[string]interface{}{
			"kind":        "skill_proposal",
			"crew_id":     crewID,
			"file_name":   fileName,
			"slug":        staged.Slug,
			"scan_status": staged.Scan.Status,
		},
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"file_name":      fileName,
		"slug":           staged.Slug,
		"scan_status":    staged.Scan.Status,
		"scan_reason":    staged.Scan.Reason,
		"decision":       string(gate.Decision),
		"autonomy_level": string(gate.Level),
		// Always true: this route only ever stages. Named explicitly so a
		// caller can rely on the same field across all six gated routes.
		"pending_review": true,
	})
}

// stageProposedSkill stages content under the crew's .proposed directory and
// announces it the way Author does (journal entry + blocking, ADMIN-addressed
// inbox item that `skills proposed approve/reject` resolves).
//
// Extracted so the #1768 held arm of the internal /skills/generate route can
// land its LLM output in the same review queue instead of writing straight
// into the live skills registry. Returns the staged file name and slug.
func (h *SkillProposedHandler) stageProposedSkill(ctx context.Context, wsID, crewID, content, origin string) (fileName, slug, scanStatus, scanReason string, err error) {
	dir, derr := h.proposedDirForCrew(ctx, wsID, crewID)
	if derr != nil {
		return "", "", "", "", derr
	}
	staged, serr := skills.StageAuthoredSkill(dir, content)
	if serr != nil {
		return "", "", "", "", serr
	}
	fileName = filepath.Base(staged.Path)
	if _, emitErr := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		CrewID:      crewID,
		Type:        journal.EntryMemorySkillProposed,
		ActorType:   journal.ActorAgent,
		Severity:    journal.SeverityNotice,
		Summary:     "skill " + origin + " by agent: " + staged.Slug,
		Payload: map[string]any{
			"slug":        staged.Slug,
			"file_name":   fileName,
			"origin":      origin,
			"scan_status": staged.Scan.Status,
			"scan_reason": staged.Scan.Reason,
		},
	}); emitErr != nil {
		h.logger.Warn("skill stage emit", "err", emitErr, "origin", origin)
	}
	_ = inbox.Insert(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: wsID,
		Kind:        inbox.KindEscalation,
		SourceID:    skillProposalInboxSource(crewID, fileName),
		TargetRole:  "ADMIN",
		Title:       "Skill proposed for review: " + staged.Slug,
		BodyMD:      "An agent " + origin + " a new skill. Approve it to add it to the crew, or reject it.",
		SenderType:  "agent",
		SenderName:  "Agent skill author",
		Priority:    "high",
		Blocking:    true,
		Payload: map[string]interface{}{
			"kind":        "skill_proposal",
			"crew_id":     crewID,
			"file_name":   fileName,
			"slug":        staged.Slug,
			"origin":      origin,
			"scan_status": staged.Scan.Status,
		},
	})
	return fileName, staged.Slug, staged.Scan.Status, staged.Scan.Reason, nil
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
