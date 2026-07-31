package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// AdminKeeperFindingsHandler answers the one question an operator cannot answer
// about a security control until it fires for real: "if Keeper sees something,
// does it actually reach me?"
//
// The routing exists — keeper_request.go writes an inbox item on ESCALATE, and
// on a DENY at or above the workspace's risk threshold, targeted at the named
// security contact with a MANAGER fanout as the fallback. What was missing is any
// way to CONFIRM it before an incident does the confirming. A wrong security
// contact, a member who lost the role, an inbox nobody watches: all silent until
// the night it matters.
//
// POST /api/v1/admin/keeper/findings/test inserts ONE inbox item through the
// same inbox.Insert with the same target resolution and the same realtime
// broadcast, then returns the recipients it resolved. The only differences from a
// real finding are a `test: true` payload flag, a title that says so, and
// Blocking=false — a drill must not park a blocking item in someone's queue.
//
// It costs nothing: no model is called. The judge is not involved, because
// whether the judge works is a different question with its own answer.
type AdminKeeperFindingsHandler struct {
	db          *sql.DB
	journal     journal.Emitter
	broadcaster KeeperBroadcaster
	logger      *slog.Logger
}

func NewAdminKeeperFindingsHandler(db *sql.DB, j journal.Emitter, b KeeperBroadcaster, logger *slog.Logger) *AdminKeeperFindingsHandler {
	return &AdminKeeperFindingsHandler{db: db, journal: j, broadcaster: b, logger: logger}
}

// keeperFindingsRecipient is one person the finding will reach, and why.
type keeperFindingsRecipient struct {
	UserID string `json:"user_id"`
	Email  string `json:"email,omitempty"`
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	// Reason is "security contact" (named on the governance row) or "role
	// fanout" (reached because MANAGER-and-above see MANAGER-targeted items).
	Reason string `json:"reason"`
}

type keeperFindingsTestResponse struct {
	InboxItemID string                    `json:"inbox_item_id"`
	Recipients  []keeperFindingsRecipient `json:"recipients"`
	// SecurityContactUserID is empty when the workspace never named one, in
	// which case the fanout below is the whole routing.
	SecurityContactUserID string `json:"security_contact_user_id,omitempty"`
	// Warning is set when the item resolved to nobody — the finding would be
	// written and seen by no one, which is worth saying out loud.
	Warning string `json:"warning,omitempty"`
}

// SendTest inserts a synthetic finding and reports where it landed.
// POST /api/v1/admin/keeper/findings/test
func (h *AdminKeeperFindingsHandler) SendTest(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "Keeper findings routing is not available")
		return
	}

	actor := ""
	actorName := "an administrator"
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
		if u.Email != "" {
			actorName = u.Email
		}
	}

	gov := governance.Resolve(r.Context(), h.db, h.logger, workspaceID)
	recipients, err := h.resolveRecipients(r, workspaceID, gov.SecurityContactUserID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve keeper finding recipients", err)
		return
	}

	// Unique per send: inbox.Insert derives the row id from (kind, source_id)
	// and INSERT OR IGNOREs, so a fixed source id would make the second test
	// silently write nothing — the worst possible outcome for a test button.
	sourceID := "keepertest_" + generateCUID()
	item := inbox.Item{
		WorkspaceID:  workspaceID,
		Kind:         inbox.KindEscalation,
		SourceID:     sourceID,
		TargetUserID: gov.SecurityContactUserID,
		TargetRole:   "MANAGER",
		Title:        "Keeper test finding — routing check, no action needed",
		BodyMD: "This is a **test** finding sent from Admin → Keeper by " + actorName + ".\n\n" +
			"It travelled the same path a real Keeper escalation takes: same inbox writer, same " +
			"target resolution, same realtime push. If you are reading it, Keeper can reach you.\n\n" +
			"Nothing was evaluated and no model was called.",
		SenderType: "system",
		SenderID:   "keeper",
		SenderName: "Keeper",
		Priority:   "low",
		// Never blocking: a drill that parks a blocking item in someone's queue
		// is a drill that gets the feature turned off.
		Blocking: false,
		Payload: map[string]interface{}{
			"test":         true,
			"request_type": "findings_test",
			"sent_by":      actor,
			"reason":       "operator-initiated routing check",
		},
	}
	if err := inbox.Insert(r.Context(), h.db, h.logger, item); err != nil {
		replyInternalError(w, h.logger, "insert keeper test finding", err)
		return
	}
	if h.broadcaster != nil {
		h.broadcaster.BroadcastInboxUpdated(workspaceID, "keeper")
	}

	resp := keeperFindingsTestResponse{
		InboxItemID:           "ibx_" + inbox.KindEscalation + "_" + sourceID,
		Recipients:            recipients,
		SecurityContactUserID: gov.SecurityContactUserID,
	}
	if len(recipients) == 0 {
		// Reported, not an error: the write succeeded and the item exists. The
		// finding simply has no audience, which is exactly the misconfiguration
		// this endpoint exists to surface.
		resp.Warning = "This finding reached nobody: no security contact is set and this workspace has no member with MANAGER, ADMIN or OWNER role."
	}

	h.logger.Info("keeper: test finding sent",
		"workspace_id", workspaceID, "actor", actor, "recipients", len(recipients))
	if h.journal != nil {
		if _, err := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: workspaceID,
			Type:        journal.EntryKeeperDecision,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorUser,
			ActorID:     actor,
			Summary:     "keeper test finding sent to verify inbox routing",
			Payload: map[string]any{
				"recipients": len(recipients),
				"test":       true,
				"rule":       "keeper_findings_test",
			},
			Refs: map[string]any{"inbox_item_id": resp.InboxItemID},
		}); err != nil {
			h.logger.Warn("keeper findings test: journal emit failed", "error", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolveRecipients lists who will actually see the item, mirroring
// inboxVisibilityClause: the named target user, plus every member whose role
// rank reaches the MANAGER rank the item is fanned out to.
//
// Deliberately computed from the same rank table the visibility filter uses
// rather than a hardcoded role list — a preview that disagrees with the filter
// is worse than no preview.
func (h *AdminKeeperFindingsHandler) resolveRecipients(r *http.Request, workspaceID, contactUserID string) ([]keeperFindingsRecipient, error) {
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wm.user_id, wm.role, COALESCE(u.email, ''), COALESCE(u.full_name, '')
		  FROM workspace_members wm
		  JOIN users u ON u.id = wm.user_id
		 WHERE wm.workspace_id = ?
		 ORDER BY wm.role, u.email`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list workspace members: %w", err)
	}
	defer rows.Close()

	fanoutRank := roleRank["MANAGER"]
	out := []keeperFindingsRecipient{}
	seenContact := false
	for rows.Next() {
		var userID, role, email, name string
		if err := rows.Scan(&userID, &role, &email, &name); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		isContact := contactUserID != "" && userID == contactUserID
		byFanout := roleRank[strings.ToUpper(role)] >= fanoutRank
		if !isContact && !byFanout {
			continue
		}
		reason := "role fanout"
		if isContact {
			reason = "security contact"
			seenContact = true
		}
		out = append(out, keeperFindingsRecipient{
			UserID: userID, Email: email, Name: name, Role: role, Reason: reason,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}

	// A contact who is no longer a member of this workspace cannot see the item
	// at all. Saying so beats listing them as a recipient — that is the exact
	// stale-configuration case the preview is for.
	if contactUserID != "" && !seenContact {
		out = append(out, keeperFindingsRecipient{
			UserID: contactUserID,
			Reason: "security contact — NOT a member of this workspace, so they will not see it",
		})
	}
	return out, nil
}
