package api

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func memberCreateActorIn(ctx context.Context, tx *sql.Tx, actor, workspace, role string) error {
	live, err := invitationIssuerRole(ctx, tx, actor, workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ErrDenied
	}
	if err != nil {
		return err
	}
	if role == "ADMIN" && live != "OWNER" {
		return access.ErrDenied
	}
	return nil
}

// Both writers own the transaction and run the returned notification only after
// COMMIT. Read normalized stored rights after ReplaceIn, never request JSON.
func (h *WorkspaceHandler) appendMemberCreatedIn(ctx context.Context, tx *sql.Tx, actor, workspace, user, role, stamp string, expected access.Membership, provenance ...memberCreationProvenance) (func(), error) {
	type row struct {
		Role, Mode, Created, Updated string
		Revision                     int64
		Capabilities                 sql.NullString
	}
	read := func() (row, access.Policy, error) {
		var value row
		err := tx.QueryRowContext(ctx, `SELECT role,access_mode,access_revision,created_at,updated_at,capabilities FROM workspace_members WHERE id=? AND workspace_id=? AND user_id=?`, expected.ID, workspace, user).Scan(&value.Role, &value.Mode, &value.Revision, &value.Created, &value.Updated, &value.Capabilities)
		if err != nil {
			return value, access.Policy{}, err
		}
		if value.Role != role || value.Mode != expected.Mode || value.Revision != expected.Revision || value.Created != stamp || value.Updated != stamp || value.Capabilities.Valid {
			return value, access.Policy{}, errors.New("new membership did not persist exactly")
		}
		policy, err := (access.Store{DB: h.db}).PolicyIn(ctx, tx, actor, user, workspace)
		return value, policy, err
	}
	before, policy, err := read()
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"member_id": expected.ID, "subject_user_id": user, "action": "added", "role": role, "access_mode": policy.Mode, "rights": policy.Rights}
	if len(provenance) > 0 {
		payload["created_user"] = provenance[0].CreatedUser
		payload["setup_link_issued"] = provenance[0].SetupLinkIssued
	}
	notify, err := appendAuthorizationAudit(ctx, tx, h.policyJournal, journal.Entry{
		WorkspaceID: workspace, Type: journal.EntryMemberAdded, ActorType: journal.ActorUser, ActorID: actor, Summary: "Workspace member added",
		Payload: payload,
	})
	if err != nil {
		return nil, err
	}
	after, saved, err := read()
	if err != nil {
		return nil, err
	}
	if before != after || !reflect.DeepEqual(policy.Rights, saved.Rights) {
		return nil, errors.New("membership or initial rights changed during audit")
	}
	return notify, nil
}

// Only non-secret provisioning facts belong in this canonical event.
type memberCreationProvenance struct{ CreatedUser, SetupLinkIssued bool }
