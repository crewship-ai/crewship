package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

type invitationAuditContextKey struct{}
type invitationAuditBatch struct {
	emitter       journal.Emitter
	notifications []func()
}

func withInvitationAudit(ctx context.Context, emitter journal.Emitter) (context.Context, func()) {
	batch := &invitationAuditBatch{emitter: emitter}
	return context.WithValue(ctx, invitationAuditContextKey{}, batch), func() {
		for _, notify := range batch.notifications {
			notify()
		}
	}
}

// Only fixed, non-secret identifiers enter the journal. No proof, digest, email
// or request payload is copied into an audit entry.
func appendInvitationAudit(ctx context.Context, tx *sql.Tx, kind journal.EntryType, workspace, actor, id, role, subject string) error {
	// Proof and email stay in this transaction-local receipt only. They must
	// never enter the canonical/public payload, logs or returned errors.
	type invitationRow struct {
		ID, Workspace, Email, Role, Issuer, Proof, Expires, Created, Mode, Rights string
		Accepted                                                                  sql.NullString
	}
	read := func() (invitationRow, error) {
		var value invitationRow
		err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,email,role,invited_by,token,expires_at,created_at,access_mode,rights_json,accepted_at
   FROM workspace_invitations WHERE id=? AND workspace_id=?`, id, workspace).Scan(
			&value.ID, &value.Workspace, &value.Email, &value.Role, &value.Issuer, &value.Proof, &value.Expires, &value.Created, &value.Mode, &value.Rights, &value.Accepted)
		return value, err
	}
	before, err := read()
	if err != nil {
		return err
	}
	if before.Role != role {
		return errors.New("stored invitation role changed")
	}
	var membership map[string]any
	if kind == journal.EntryInvitationAccepted {
		if subject == "" || !before.Accepted.Valid {
			return errors.New("invitation acceptance not retained")
		}
		membership, err = memberDepartureSnapshot(ctx, tx, workspace, subject)
		if err != nil {
			return err
		}
	}
	mode, raw := before.Mode, before.Rights
	rights := []access.Right{}
	if err := json.Unmarshal([]byte(raw), &rights); err != nil {
		return err
	}
	if (mode != "trusted" && mode != "restricted") || rights == nil {
		return errors.New("invalid stored invitation policy")
	}
	e := journal.Entry{WorkspaceID: workspace, Type: kind, ActorType: journal.ActorUser, ActorID: actor, Summary: string(kind), Payload: map[string]any{"invitation_id": id, "role": role, "subject_user_id": subject, "access_mode": mode, "rights": rights}}
	batch, _ := ctx.Value(invitationAuditContextKey{}).(*invitationAuditBatch)
	appendTx := journal.AppendTx
	if batch != nil && batch.emitter != nil {
		emitter, ok := batch.emitter.(journal.TransactionEmitter)
		if !ok {
			return fmt.Errorf("invitation journal does not support atomic writes")
		}
		appendTx = emitter.AppendTx
	}
	notify, err := appendTx(ctx, tx, e)
	if err != nil {
		return err
	}
	after, err := read()
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("invitation changed during audit")
	}
	if kind == journal.EntryInvitationAccepted {
		saved, err := memberDepartureSnapshot(ctx, tx, workspace, subject)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(membership, saved) {
			return errors.New("accepted membership changed during audit")
		}
	}
	if batch != nil {
		batch.notifications = append(batch.notifications, notify)
	}
	return nil
}
