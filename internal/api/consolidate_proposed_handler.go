package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ProposedHandler serves the HITL surface for memory consolidation
// proposals: approve, reject, and explain. The handler is intentionally
// thin — the heavy lifting (atomic merge, DB transitions, journal
// emit) lives in internal/consolidate/approve.go where it can be
// re-used by a future CLI subcommand or a webhook integration without
// having to fake an HTTP request.
//
// Auth: every method requires workspace context + OWNER/ADMIN role.
// The proposal id arrives as a path param ({id}). The handler does NOT
// verify the proposal belongs to the caller's workspace — that check
// lives in the helper to keep authz close to the data lookup (so a
// cross-workspace probe gets the same 404 as a non-existent id, no
// existence leak).
type ProposedHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewProposedHandler(db *sql.DB, logger *slog.Logger) *ProposedHandler {
	return &ProposedHandler{
		db:      db,
		logger:  logger,
		journal: noopEmitter{},
	}
}

// SetJournal wires the production emitter once the Router has one.
// Mirrors the pattern in ConsolidateHandler so the two handlers can
// share the same journal infrastructure without re-allocating it.
func (h *ProposedHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// Approve serves POST /api/v1/consolidate/proposed/{id}/approve.
// Body is ignored (the proposal id + caller identity are the entire
// state machine). Returns 200 with the merged canonical path on
// success, 404 on missing proposal, 409 on already-decided, 403 on
// non-OWNER/ADMIN.
func (h *ProposedHandler) Approve(w http.ResponseWriter, r *http.Request) {
	user, role, ok := h.requireOwnerOrAdmin(w, r)
	if !ok {
		return
	}
	proposalID := r.PathValue("id")
	if proposalID == "" {
		replyError(w, http.StatusBadRequest, "proposal id required")
		return
	}

	// Workspace boundary check goes BEFORE any state change. We look
	// the proposal up via the read-only Explain helper first; a
	// cross-workspace probe gets 404 here without ever flipping the
	// row state. This pattern matches Reject below — both write paths
	// short-circuit on cross-workspace mismatch before touching DB or
	// canonical files.
	pre, err := consolidate.ExplainProposal(r.Context(), h.db, proposalID)
	if err != nil {
		h.mapDecisionError(w, err, "approve lookup")
		return
	}
	if !h.assertWorkspaceMatch(w, pre.WorkspaceID, r) {
		_ = role // silence unused; role is enforced via requireOwnerOrAdmin
		return
	}
	if pre.Status != "pending" {
		// Defensive: if the read-only lookup already shows the row
		// is decided, short-circuit with 409 rather than enter the
		// helper just to receive ErrProposalNotPending. Keeps the
		// error path narrow for racy clients.
		replyError(w, http.StatusConflict, "memory proposal already decided")
		return
	}

	res, err := consolidate.ApproveProposal(r.Context(), h.db, h.journal, h.logger, proposalID, user.ID)
	if err != nil {
		h.mapDecisionError(w, err, "approve")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"proposal_id":    res.ProposalID,
		"canonical_path": res.CanonicalPath,
		"rules_merged":   res.RulesMerged,
		"workspace_id":   res.WorkspaceID,
		"crew_id":        res.CrewID,
		"decided_by":     user.ID,
	})
}

// Reject serves POST /api/v1/consolidate/proposed/{id}/reject.
// Optional JSON body {"reason": "..."} — when supplied, the reason
// is logged with the decision (and will be persisted on a future
// memory_proposals.reason column without changing this endpoint's
// shape).
func (h *ProposedHandler) Reject(w http.ResponseWriter, r *http.Request) {
	user, _, ok := h.requireOwnerOrAdmin(w, r)
	if !ok {
		return
	}
	proposalID := r.PathValue("id")
	if proposalID == "" {
		replyError(w, http.StatusBadRequest, "proposal id required")
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		// Reject mode: a malformed body shouldn't 500; treat
		// json-decode failure as "no reason provided" so the
		// happy path is permissive.
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	// We look up the proposal first to verify it belongs to the
	// caller's workspace, then reject. Doing the workspace check
	// up-front matches the explain endpoint's pattern and means a
	// cross-workspace caller sees 404 without ever flipping state.
	exp, err := consolidate.ExplainProposal(r.Context(), h.db, proposalID)
	if err != nil {
		h.mapDecisionError(w, err, "reject lookup")
		return
	}
	if !h.assertWorkspaceMatch(w, exp.WorkspaceID, r) {
		return
	}

	if err := consolidate.RejectProposal(r.Context(), h.db, h.journal, h.logger, proposalID, user.ID, body.Reason); err != nil {
		h.mapDecisionError(w, err, "reject")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"proposal_id": proposalID,
		"status":      "rejected",
		"decided_by":  user.ID,
		"reason":      body.Reason,
	})
}

// Explain serves GET /api/v1/consolidate/proposed/{id}/explain.
// Returns the proposal row + evidence_json for the HITL review UI.
// Read-only; no side effects. Available to MEMBER+ inside the
// workspace — operators reviewing what was proposed don't need
// approval authority to look.
func (h *ProposedHandler) Explain(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	proposalID := r.PathValue("id")
	if proposalID == "" {
		replyError(w, http.StatusBadRequest, "proposal id required")
		return
	}

	exp, err := consolidate.ExplainProposal(r.Context(), h.db, proposalID)
	if err != nil {
		h.mapDecisionError(w, err, "explain")
		return
	}
	if exp.WorkspaceID != wsID {
		// Cross-workspace probe: same 404 as a missing id.
		replyError(w, http.StatusNotFound, "memory proposal not found")
		return
	}
	writeJSON(w, http.StatusOK, exp)
}

// requireOwnerOrAdmin pulls auth context, enforces role gate, and
// returns the user struct + role for callers. The boolean third
// return is the "continue" signal — if false, the response has
// already been written.
func (h *ProposedHandler) requireOwnerOrAdmin(w http.ResponseWriter, r *http.Request) (*AuthUser, string, bool) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return nil, "", false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "user required")
		return nil, "", false
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "memory proposal decisions require OWNER or ADMIN role")
		return nil, "", false
	}
	return user, role, true
}

// assertWorkspaceMatch checks the proposal's workspace against the
// caller's context. Mismatch surfaces as 404 (not 403) so the existence
// of cross-workspace rows is not observable. Returns true when matched.
func (h *ProposedHandler) assertWorkspaceMatch(w http.ResponseWriter, proposalWorkspaceID string, r *http.Request) bool {
	if proposalWorkspaceID == WorkspaceIDFromContext(r.Context()) {
		return true
	}
	replyError(w, http.StatusNotFound, "memory proposal not found")
	return false
}

// mapDecisionError translates the consolidate package's sentinel
// errors into HTTP status codes. Anything not recognised falls back
// to 500 with the error string logged (not echoed).
func (h *ProposedHandler) mapDecisionError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, consolidate.ErrProposalNotFound):
		replyError(w, http.StatusNotFound, "memory proposal not found")
	case errors.Is(err, consolidate.ErrProposalNotPending):
		replyError(w, http.StatusConflict, "memory proposal already decided")
	default:
		h.logger.Error("memory proposal "+op+" failed", "error", err)
		replyError(w, http.StatusInternalServerError, "decision failed")
	}
}
