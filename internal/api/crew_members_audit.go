package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Crew deletion and role assignment keep their existing role floors. Re-read
// live authority and resource state on the same writer as the mutation.
func (h *CrewHandler) beginMemberMutation(w http.ResponseWriter, r *http.Request, crewID, permission string) (*sql.Tx, string) {
	return h.beginCrewMutation(w, r, crewID, permission, false)
}

// Only profile/configuration and ordinary membership changes opt in. Crew
// deletion and role assignment keep the original workspace role floor.
func (h *CrewHandler) beginCrewManageMutation(w http.ResponseWriter, r *http.Request, crewID, permission string) (*sql.Tx, string) {
	return h.beginCrewMutation(w, r, crewID, permission, true)
}

func (h *CrewHandler) beginCrewMutation(w http.ResponseWriter, r *http.Request, crewID, permission string, allowGrant bool) (*sql.Tx, string) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin crew membership mutation", err)
		return nil, ""
	}
	userID := ""
	if user := UserFromContext(r.Context()); user != nil {
		userID = user.ID
	}
	var role string
	err = tx.QueryRowContext(r.Context(), `SELECT m.role FROM workspace_members m
JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL
WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(
 SELECT 1 FROM workspace_members guest WHERE guest.user_id=m.user_id AND guest.access_mode='restricted')`, WorkspaceIDFromContext(r.Context()), userID).Scan(&role)
	if err == nil && !canRole(role, permission) {
		err = access.ErrDenied
		if allowGrant {
			err = (access.Store{DB: h.db}).CrewManageIn(r.Context(), tx, userID, WorkspaceIDFromContext(r.Context()), crewID)
		}
	}
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, access.ErrDenied) {
		_ = tx.Rollback()
		replyError(w, http.StatusForbidden, "Forbidden")
		return nil, ""
	}
	if err == nil {
		var id string
		err = tx.QueryRowContext(r.Context(), `SELECT id FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crewID, WorkspaceIDFromContext(r.Context())).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			replyError(w, http.StatusNotFound, "Crew not found")
			return nil, ""
		}
	}
	if err != nil {
		_ = tx.Rollback()
		replyInternalError(w, h.logger, "read current crew membership authority", err)
		return nil, ""
	}
	return tx, role
}

func (h *CrewHandler) crewManageRequest(w http.ResponseWriter, r *http.Request, crewID, permission string) bool {
	if canRole(RoleFromContext(r.Context()), permission) {
		return true
	}
	u := UserFromContext(r.Context())
	if u != nil {
		err := (access.Store{DB: h.db}).CrewManage(r.Context(), u.ID, WorkspaceIDFromContext(r.Context()), crewID)
		if err == nil {
			return true
		}
		if !errors.Is(err, access.ErrDenied) {
			replyInternalError(w, h.logger, "read crew Manage grant", err)
			return false
		}
	}
	replyError(w, http.StatusForbidden, "Forbidden")
	return false
}

// Recheck after audit hooks as well: a hook must not revoke the issuer, grant
// or recipient and still allow the protected mutation to commit.
func (h *CrewHandler) crewMutationActorIn(r *http.Request, tx *sql.Tx, crewID, permission string, allowGrant bool) error {
	u := UserFromContext(r.Context())
	if u == nil {
		return sql.ErrNoRows
	}
	var role string
	err := tx.QueryRowContext(r.Context(), `SELECT wm.role FROM workspace_members wm
JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL
JOIN workspaces w ON w.id=wm.workspace_id AND w.deleted_at IS NULL
JOIN crews c ON c.workspace_id=w.id AND c.id=? AND c.deleted_at IS NULL
WHERE wm.user_id=? AND wm.workspace_id=?
AND NOT EXISTS(SELECT 1 FROM workspace_members ceiling WHERE ceiling.user_id=wm.user_id AND ceiling.access_mode='restricted')`, crewID, u.ID, WorkspaceIDFromContext(r.Context())).Scan(&role)
	if err != nil || canRole(role, permission) {
		return err
	}
	if allowGrant {
		return (access.Store{DB: h.db}).CrewManageIn(r.Context(), tx, u.ID, WorkspaceIDFromContext(r.Context()), crewID)
	}
	return sql.ErrNoRows
}

type crewMembershipState struct {
	ID, CrewID, UserID, CreatedAt string
	Role                          sql.NullString
}

func readCrewMembership(ctx context.Context, tx *sql.Tx, crewID, memberID string) (*crewMembershipState, error) {
	var state crewMembershipState
	err := tx.QueryRowContext(ctx, `SELECT id,crew_id,user_id,role,created_at FROM crew_members WHERE id=? AND crew_id=?`, memberID, crewID).Scan(&state.ID, &state.CrewID, &state.UserID, &state.Role, &state.CreatedAt)
	return &state, err
}

// A missing membership differs from a present membership inheriting a NULL
// role. No credential, profile or other user data belongs in this payload.
func crewMembershipPayload(state *crewMembershipState) any {
	if state == nil {
		return nil
	}
	var role any
	if state.Role.Valid {
		role = state.Role.String
	}
	return map[string]any{"role": role}
}

func (h *CrewHandler) commitMemberMutation(r *http.Request, tx *sql.Tx, action string, before, after *crewMembershipState) error {
	identity := after
	if identity == nil {
		identity = before
	}
	var notify func()
	if before == nil || after == nil || *before != *after {
		var err error
		notify, err = appendAuthorizationAudit(r.Context(), tx, h.policyJournal, journal.Entry{
			WorkspaceID: WorkspaceIDFromContext(r.Context()), CrewID: identity.CrewID,
			Type: journal.EntryCrewMemberChanged, ActorType: journal.ActorUser,
			ActorID: UserFromContext(r.Context()).ID, Summary: "Crew membership changed",
			Payload: map[string]any{"subject_user_id": identity.UserID, "member_id": identity.ID, "action": action,
				"previous_membership": crewMembershipPayload(before), "membership": crewMembershipPayload(after)},
		})
		if err != nil {
			return err
		}
	}
	if after == nil {
		// Removing the old ID is insufficient: an audit trigger could
		// restore the same subject's membership under a different ID.
		var count int
		err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM crew_members WHERE crew_id=? AND user_id=?`, identity.CrewID, identity.UserID).Scan(&count)
		if err != nil || count != 0 {
			return errors.Join(err, errors.New("removed crew membership survived"))
		}
	} else {
		saved, err := readCrewMembership(r.Context(), tx, identity.CrewID, identity.ID)
		if err != nil || *saved != *after {
			return errors.Join(err, errors.New("crew membership did not persist"))
		}
	}
	permission, allowGrant := "manage", action == "removed"
	if action == "added" {
		permission, allowGrant = "create", !after.Role.Valid
	}
	if err := h.crewMutationActorIn(r, tx, identity.CrewID, permission, allowGrant); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if notify != nil {
		notify()
	}
	return nil
}
