package api

// Keeper watchdog governance settings (issue #1001, M0): the OWNER/ADMIN
// in-app toggle, the named security contact the watchdog snitches to, and
// the DENY-notify risk threshold. GET/PUT /api/v1/admin/keeper/governance.
//
// No row = "inherit the server config" — the GET reports configured=false so
// the UI can render the inherited state (combined with GET /system/keeper);
// the first PUT creates the explicit row, which wins from then on.

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

type keeperGovernancePutBody struct {
	Enabled               bool   `json:"enabled"`
	SecurityContactUserID string `json:"security_contact_user_id"`
	DenyNotifyMinRisk     *int   `json:"deny_notify_min_risk"`
}

// Put handles PUT /api/v1/admin/keeper/governance (roleManage = OWNER/ADMIN).
func (h *KeeperGovernanceHandler) Put(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var body keeperGovernancePutBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	risk := governance.DefaultDenyNotifyMinRisk
	if body.DenyNotifyMinRisk != nil {
		risk = *body.DenyNotifyMinRisk
		if risk < 1 || risk > 10 {
			replyError(w, http.StatusBadRequest, "deny_notify_min_risk must be between 1 and 10")
			return
		}
	}

	// The security contact must be an OWNER/ADMIN member of this workspace —
	// snitching to someone who can't act on (or shouldn't see) escalations
	// is a config foot-gun, so reject it at write time.
	if body.SecurityContactUserID != "" {
		var role string
		err := h.db.QueryRowContext(r.Context(), `
			SELECT role FROM workspace_members
			WHERE workspace_id = ? AND user_id = ?`,
			wsID, body.SecurityContactUserID).Scan(&role)
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

	s := governance.Settings{
		Enabled:               body.Enabled,
		SecurityContactUserID: body.SecurityContactUserID,
		DenyNotifyMinRisk:     risk,
	}
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
		},
	}); jerr != nil {
		h.logger.Warn("keeper governance: journal emit failed", "error", jerr)
	}

	writeJSON(w, http.StatusOK, keeperGovernanceResponse{Configured: true, Settings: s})
}
