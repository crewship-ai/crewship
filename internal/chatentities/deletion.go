package chatentities

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// DeletionAccessSnapshot preserves the access boundary after hard deletion.
// Version 1 is valid only for ordinary trusted workspace access. Restricted
// sessions and future finer-grained issue boundaries must fail closed until a
// version explicitly describing those boundaries is implemented.
type DeletionAccessSnapshot struct {
	Version     int    `json:"version"`
	WorkspaceID string `json:"workspace_id"`
	Visibility  string `json:"visibility"`
}

func ParseDeletionAccessSnapshot(raw json.RawMessage) (DeletionAccessSnapshot, error) {
	var s DeletionAccessSnapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&s); e != nil {
		return s, ErrInvalid
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return s, ErrInvalid
	}
	if s.Version != 1 || s.WorkspaceID == "" || s.Visibility != "trusted_workspace" {
		return s, ErrInvalid
	}
	return s, nil
}

// AppendDeletionRefs records a human deletion in every chat which previously
// referenced this issue. It must share the caller's deletion transaction.
// Ref.RunID is the original chat anchor, never the identity of the deleter:
// the actor and timestamp come from the new human operation. Normal verified
// run references must continue through AppendRef with its stricter run guard.
func AppendDeletionRefs(ctx context.Context, tx *sql.Tx, operationID, workspaceID, entityID string) ([]Ref, error) {
	o, e := GetOperation(ctx, tx, workspaceID, operationID)
	if e != nil {
		return nil, e
	}
	if o.ActorType != "human" || o.Provenance != "human" || o.Kind != "issue" || o.Verb != "deleted" || o.EntityID != entityID || o.RunID != "" {
		return nil, ErrInvalid
	}
	snapshot, e := ParseDeletionAccessSnapshot(o.AccessSnapshot)
	if e != nil || snapshot.WorkspaceID != workspaceID {
		return nil, ErrInvalid
	}
	// One original anchor per chat, regardless of how many prior changes or
	// runs referenced the issue. Workspace checks also fence corrupted links.
	rows, e := tx.QueryContext(ctx, `SELECT chat_id,run_id,trigger_message_id FROM (
 SELECT r.chat_id,r.run_id,r.trigger_message_id,
 ROW_NUMBER() OVER(PARTITION BY r.chat_id ORDER BY r.chat_seq) AS ordinal
 FROM chat_entity_refs r JOIN agent_operations prior ON prior.id=r.operation_id
 JOIN chats c ON c.id=r.chat_id AND c.workspace_id=prior.workspace_id
 WHERE prior.workspace_id=? AND prior.kind='issue' AND prior.entity_id=?
 AND prior.status='completed' AND prior.provenance='verified' AND prior.verb<>'deleted'
 AND r.run_id<>'' AND r.trigger_message_id<>''
 ) WHERE ordinal=1 ORDER BY chat_id`, workspaceID, entityID)
	if e != nil {
		return nil, e
	}
	type anchor struct{ chat, run, message string }
	anchors := make([]anchor, 0)
	for rows.Next() {
		var a anchor
		if e = rows.Scan(&a.chat, &a.run, &a.message); e != nil {
			rows.Close()
			return nil, e
		}
		anchors = append(anchors, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	refs := make([]Ref, 0, len(anchors))
	for _, a := range anchors {
		var random [16]byte
		if _, e = rand.Read(random[:]); e != nil {
			return nil, e
		}
		eventID := "c" + hex.EncodeToString(random[:])
		_, e = tx.ExecContext(ctx, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(chat_id,operation_id) DO NOTHING`, eventID, a.chat, o.ID, a.run, a.message, tsformat.Format(time.Now()))
		if e != nil {
			return nil, e
		}
		var r Ref
		e = tx.QueryRowContext(ctx, `SELECT event_id,chat_id,operation_id,run_id,trigger_message_id,chat_seq,created_at FROM chat_entity_refs WHERE chat_id=? AND operation_id=?`, a.chat, o.ID).Scan(&r.EventID, &r.ChatID, &r.OperationID, &r.RunID, &r.TriggerMessageID, &r.Seq, &r.CreatedAt)
		if e != nil {
			return nil, e
		}
		if r.RunID != a.run || r.TriggerMessageID != a.message {
			return nil, ErrKeyConflict
		}
		r.Operation = *o
		refs = append(refs, r)
	}
	return refs, nil
}
