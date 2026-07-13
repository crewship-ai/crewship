package api

// Keeper watchdog governance settings (issue #1001, M0): the OWNER/ADMIN
// in-app toggle, the named security contact the watchdog snitches to, and
// the DENY-notify risk threshold. GET/PUT /api/v1/admin/keeper/governance.
//
// No row = the watchdog is OFF for the workspace (opt-in, default OFF) — the
// GET reports configured=false + enabled=false; the first PUT creates the
// explicit row. PUT is a partial update: only the fields the caller sends are
// merged, so single-field edits don't clobber each other.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

type KeeperGovernanceHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewKeeperGovernanceHandler(db *sql.DB, logger *slog.Logger, j journal.Emitter) *KeeperGovernanceHandler {
	if j == nil {
		j = noopEmitter{}
	}
	return &KeeperGovernanceHandler{db: db, logger: logger, journal: j}
}

type keeperGovernanceResponse struct {
	Configured bool `json:"configured"`
	governance.Settings
}

// Get handles GET /api/v1/admin/keeper/governance (ADMIN+).
func (h *KeeperGovernanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	s, found, err := governance.Get(r.Context(), h.db, wsID)
	if err != nil {
		h.logger.Error("keeper governance: get", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, keeperGovernanceResponse{Configured: found, Settings: s})
}

// keeperGovernancePutBody is a partial update: every field is a pointer, so a
// nil field is "leave unchanged" and only the fields the caller sends are
// merged onto the existing row. This lets each CLI subcommand
// (enable/disable/contact/threshold) send exactly its one field without a
// read-merge-write round-trip — which both removes the foot-gun where a stale
// read clobbers the enabled flag and makes concurrent single-field edits
// commute instead of overwriting each other.
type keeperGovernancePutBody struct {
	Enabled               *bool     `json:"enabled"`
	SecurityContactUserID *string   `json:"security_contact_user_id"`
	DenyNotifyMinRisk     *int      `json:"deny_notify_min_risk"`
	WatchSpec             *string   `json:"watch_spec"`
	WatchPresets          *[]string `json:"watch_presets"`
	RequireSecondApprover *bool     `json:"require_second_approver"`
}

// Put handles PUT /api/v1/admin/keeper/governance (roleManage = OWNER/ADMIN).
// Partial-update semantics: merges the provided fields onto the current row
// (or the opt-in defaults for a first write) so a caller never has to echo
// back settings it isn't changing.
func (h *KeeperGovernanceHandler) Put(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var body keeperGovernancePutBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Start from the current row (or the opt-in defaults: disabled, default
	// threshold, no contact) and apply only what the caller sent.
	cur, _, err := governance.Get(r.Context(), h.db, wsID)
	if err != nil {
		h.logger.Error("keeper governance: load current", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if body.Enabled != nil {
		cur.Enabled = *body.Enabled
	}
	if body.DenyNotifyMinRisk != nil {
		if *body.DenyNotifyMinRisk < 1 || *body.DenyNotifyMinRisk > 10 {
			replyError(w, http.StatusBadRequest, "deny_notify_min_risk must be between 1 and 10")
			return
		}
		cur.DenyNotifyMinRisk = *body.DenyNotifyMinRisk
	}
	if body.SecurityContactUserID != nil {
		cur.SecurityContactUserID = *body.SecurityContactUserID
	}
	if body.WatchSpec != nil {
		// Cap server-side so the DB row and the compiled prompt block stay
		// bounded regardless of what the CLI/UI send.
		if len(*body.WatchSpec) > governance.MaxWatchSpecLen {
			replyError(w, http.StatusBadRequest, "watch_spec exceeds the maximum length")
			return
		}
		cur.WatchSpec = *body.WatchSpec
	}
	if body.WatchPresets != nil {
		if err := governance.ValidatePresets(*body.WatchPresets); err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		cur.WatchPresets = *body.WatchPresets
	}
	if body.RequireSecondApprover != nil {
		cur.RequireSecondApprover = *body.RequireSecondApprover
	}

	// The security contact must be an OWNER/ADMIN member of this workspace —
	// snitching to someone who can't act on (or shouldn't see) escalations
	// is a config foot-gun, so reject it at write time. Validate ONLY when this
	// request is setting/changing the contact (body.SecurityContactUserID != nil):
	// an unrelated partial update (e.g. watch_spec/presets) must not be blocked
	// because a previously-stored contact was demoted since it was last validated.
	if body.SecurityContactUserID != nil && cur.SecurityContactUserID != "" {
		var role string
		err := h.db.QueryRowContext(r.Context(), `
			SELECT role FROM workspace_members
			WHERE workspace_id = ? AND user_id = ?`,
			wsID, cur.SecurityContactUserID).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "security contact is not a member of this workspace")
			return
		}
		if err != nil {
			h.logger.Error("keeper governance: contact lookup", "error", err)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if role != "OWNER" && role != "ADMIN" {
			replyError(w, http.StatusBadRequest, "security contact must have OWNER or ADMIN role")
			return
		}
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}

	s := cur
	if err := governance.Upsert(r.Context(), h.db, wsID, s, actor); err != nil {
		h.logger.Error("keeper governance: upsert", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Governance flips are exactly the kind of event the audit trail is for:
	// who turned the watchdog on/off and where findings are routed.
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		Type:        journal.EntryKeeperDecision,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actor,
		Summary:     "keeper watchdog governance updated",
		Payload: map[string]any{
			"enabled":                  s.Enabled,
			"security_contact_user_id": s.SecurityContactUserID,
			"deny_notify_min_risk":     s.DenyNotifyMinRisk,
			// Log the shape of the watch spec, not its text — the full rules
			// can be large and needn't bloat every audit entry.
			"watch_preset_count":      len(s.WatchPresets),
			"watch_spec_len":          len(s.WatchSpec),
			"require_second_approver": s.RequireSecondApprover,
		},
	}); jerr != nil {
		h.logger.Warn("keeper governance: journal emit failed", "error", jerr)
	}

	writeJSON(w, http.StatusOK, keeperGovernanceResponse{Configured: true, Settings: s})
}
