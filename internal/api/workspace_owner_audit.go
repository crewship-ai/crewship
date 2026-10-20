package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The initial membership snapshot is durable audit data, not a public feed DTO.
// In particular, SQL's native timestamp defaults need not equal the supplied
// created_at value on the signup and bootstrap paths.
type ownerMembershipSnapshot struct {
	ID                    string   `json:"id"`
	WorkspaceID           string   `json:"workspace_id"`
	UserID                string   `json:"user_id"`
	Role                  string   `json:"role"`
	AccessMode            string   `json:"access_mode"`
	AccessRevision        int64    `json:"access_revision"`
	Capabilities          *string  `json:"capabilities"`
	EffectiveCapabilities []string `json:"effective_capabilities"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
}

func readInitialOwnerIn(ctx context.Context, tx *sql.Tx, workspace, member, user string) (ownerMembershipSnapshot, error) {
	var s ownerMembershipSnapshot
	var caps sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,user_id,role,access_mode,access_revision,capabilities,created_at,updated_at
 FROM workspace_members WHERE id=? AND workspace_id=? AND user_id=?
 AND typeof(id)='text' AND typeof(workspace_id)='text' AND typeof(user_id)='text'
 AND typeof(role)='text' AND typeof(access_mode)='text' AND typeof(access_revision)='integer'
 AND (capabilities IS NULL OR typeof(capabilities)='text')
 AND typeof(created_at)='text' AND typeof(updated_at)='text'`, member, workspace, user).Scan(
		&s.ID, &s.WorkspaceID, &s.UserID, &s.Role, &s.AccessMode, &s.AccessRevision, &caps, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return s, err
	}
	if s.Role != "OWNER" || s.AccessMode != "trusted" {
		return s, errors.New("initial owner membership changed")
	}
	if caps.Valid {
		s.Capabilities = &caps.String
	}
	s.EffectiveCapabilities = capsAsSortedSlice(resolveCapabilitiesFromRow(caps, s.Role))
	// This checks current account/global restrictions after the new OWNER row
	// exists. It is not evidence of a pre-existing grant to create a workspace.
	if _, err := invitationIssuerRole(ctx, tx, user, workspace); err != nil {
		return s, err
	}
	return s, nil
}

// All three creation handlers append on their business transaction. Bootstrap
// calls this after its instance token audit and before its final token ACK.
func appendWorkspaceOwnerAudit(ctx context.Context, tx *sql.Tx, emitter journal.Emitter, inserted sql.Result, workspace, member, user, path string) (func(), error) {
	if n, err := inserted.RowsAffected(); err != nil || n != 1 {
		return nil, errors.New("initial owner insert was not acknowledged")
	}
	snapshot, err := readInitialOwnerIn(ctx, tx, workspace, member, user)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"action": "owner_created", "creation_path": path,
		"subject_user_id": user, "member_id": member,
		"old_membership": nil, "new_membership": snapshot,
	}
	// Freeze the expected business snapshot before a configured emitter can
	// normalize or mutate the shared entry payload. JSON bytes are local only.
	expected, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	entry := journal.Entry{ID: generateCUID(), WorkspaceID: workspace, Type: journal.EntryMemberOwnerCreated,
		ActorType: journal.ActorUser, ActorID: user, Summary: "Initial workspace owner created", Payload: payload}
	notify, err := appendAuthorizationAudit(ctx, tx, emitter, entry)
	if err != nil {
		return nil, err
	}
	var persisted string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM journal_entries WHERE id=? AND workspace_id=?
 AND entry_type=? AND actor_type='user' AND actor_id=? AND summary=?`,
		entry.ID, workspace, entry.Type, user, entry.Summary).Scan(&persisted); err != nil {
		return nil, err
	}
	// The transactional writer verifies its signed native INSERT. This business
	// ACK compares the decoded snapshot, independent of JSON object key order.
	var want, actual map[string]any
	if err := json.Unmarshal(expected, &want); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(persisted), &actual); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(actual, want) {
		return nil, errors.New("initial owner audit was not acknowledged")
	}
	current, err := readInitialOwnerIn(ctx, tx, workspace, member, user)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(snapshot, current) {
		return nil, errors.New("initial owner changed during audit")
	}
	return notify, nil
}
