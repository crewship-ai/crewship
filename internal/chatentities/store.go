package chatentities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const operationColumns = `id,workspace_id,operation_key,request_hash,actor_type,actor_id,run_id,human_authority,provenance,kind,verb,entity_id,entity_revision,entity_aggregate_revision,access_snapshot,status,result,source_proposal_id,source_proposal_version,created_at`

type scanner interface{ Scan(...any) error }

func scanOperation(row scanner) (*Operation, error) {
	var o Operation
	var authority, snapshot, result string
	err := row.Scan(&o.ID, &o.WorkspaceID, &o.Key, &o.RequestHash, &o.ActorType, &o.ActorID, &o.RunID, &authority, &o.Provenance, &o.Kind, &o.Verb, &o.EntityID, &o.Revision, &o.AggregateRevision, &snapshot, &o.Status, &result, &o.SourceProposalID, &o.SourceProposalVersion, &o.CreatedAt)
	o.HumanAuthority = json.RawMessage(authority)
	o.AccessSnapshot = json.RawMessage(snapshot)
	o.Result = json.RawMessage(result)
	return &o, err
}
func jsonString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	if !json.Valid(raw) {
		return "", ErrInvalid
	}
	return string(raw), nil
}

// BeginOperation reserves a key in the caller's mutation transaction. The
// caller must roll back on any error, and must authorize before invoking it.
// A successful new operation must be completed before committing. A replay is
// only returned for a completed operation belonging to the same run.
func BeginOperation(ctx context.Context, tx *sql.Tx, s OperationSpec) (*Operation, bool, error) {
	if strings.TrimSpace(s.Key) == "" {
		return nil, false, ErrKeyRequired
	}
	if len(s.Key) > 200 || s.ID == "" || s.WorkspaceID == "" || s.RequestHash == "" || s.ActorID == "" {
		return nil, false, ErrInvalid
	}
	authority, err := jsonString(s.HumanAuthority)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_operations (id,workspace_id,operation_key,request_hash,actor_type,actor_id,run_id,human_authority,provenance,kind,verb,source_proposal_id,source_proposal_version,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,actor_type,actor_id,operation_key) DO NOTHING`, s.ID, s.WorkspaceID, s.Key, s.RequestHash, s.ActorType, s.ActorID, s.RunID, authority, s.Provenance, s.Kind, s.Verb, s.SourceProposalID, s.SourceProposalVersion, now)
	if err != nil {
		return nil, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	o, err := scanOperation(tx.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM agent_operations WHERE workspace_id=? AND actor_type=? AND actor_id=? AND operation_key=?`, s.WorkspaceID, s.ActorType, s.ActorID, s.Key))
	if err != nil {
		return nil, false, err
	}
	if n == 1 {
		return o, false, nil
	}
	if o.RequestHash != s.RequestHash || o.RunID != s.RunID || o.Kind != s.Kind || o.Verb != s.Verb {
		return nil, false, ErrKeyConflict
	}
	if o.Status != "completed" {
		return nil, false, ErrOperationIncomplete
	}
	return o, true, nil
}

// CompleteOperation stores the successful response in the same transaction as
// the mutation. Completed results cannot be overwritten.
func CompleteOperation(ctx context.Context, tx *sql.Tx, id string, s ResultSpec) error {
	if s.EntityID == "" || s.Revision < 0 || s.AggregateRevision < 0 {
		return ErrInvalid
	}
	result, err := jsonString(s.Result)
	if err != nil {
		return err
	}
	snapshot, err := jsonString(s.AccessSnapshot)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_operations SET entity_id=?,entity_revision=?,entity_aggregate_revision=?,access_snapshot=?,result=?,status='completed' WHERE id=? AND status='pending'`, s.EntityID, s.Revision, s.AggregateRevision, snapshot, result, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrOperationIncomplete
	}
	return nil
}

func GetOperation(ctx context.Context, q Queryer, workspaceID, id string) (*Operation, error) {
	return scanOperation(q.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM agent_operations WHERE workspace_id=? AND id=? AND status='completed'`, workspaceID, id))
}

// AppendRef binds an already completed operation to a chat. The workspace and
// run must match; callers separately verify that the trigger belongs to it.
// Repeating the append returns the original event rather than another card.
func AppendRef(ctx context.Context, tx *sql.Tx, s RefSpec) (Ref, error) {
	if s.EventID == "" || s.ChatID == "" || s.OperationID == "" || s.RunID == "" || s.TriggerMessageID == "" {
		return Ref{}, ErrInvalid
	}
	var valid int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_operations o JOIN chats c ON c.workspace_id=o.workspace_id WHERE o.id=? AND c.id=? AND o.run_id=? AND o.status='completed' AND o.provenance='verified'`, s.OperationID, s.ChatID, s.RunID).Scan(&valid)
	if err != nil {
		return Ref{}, err
	}
	if valid != 1 {
		return Ref{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_entity_refs(event_id,chat_id,operation_id,run_id,trigger_message_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(chat_id,operation_id) DO NOTHING`, s.EventID, s.ChatID, s.OperationID, s.RunID, s.TriggerMessageID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Ref{}, err
	}
	var r Ref
	err = tx.QueryRowContext(ctx, `SELECT event_id,chat_id,operation_id,run_id,trigger_message_id,chat_seq,created_at FROM chat_entity_refs WHERE chat_id=? AND operation_id=?`, s.ChatID, s.OperationID).Scan(&r.EventID, &r.ChatID, &r.OperationID, &r.RunID, &r.TriggerMessageID, &r.Seq, &r.CreatedAt)
	if err != nil {
		return Ref{}, err
	}
	if r.RunID != s.RunID || r.TriggerMessageID != s.TriggerMessageID {
		return Ref{}, ErrKeyConflict
	}
	var ws string
	err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM chats WHERE id=?`, s.ChatID).Scan(&ws)
	if err != nil {
		return Ref{}, err
	}
	o, err := GetOperation(ctx, tx, ws, s.OperationID)
	if err != nil {
		return Ref{}, err
	}
	r.Operation = *o
	return r, nil
}

// ScanRefs returns a bounded raw scan, not an authorized viewer projection.
// The caller must filter every row before exposing metadata. Position advances
// over hidden rows as well, and is carried only by an opaque server cursor.
func ScanRefs(ctx context.Context, q Queryer, chatID string, afterSeq int64, limit int) ([]Ref, int64, error) {
	if chatID == "" || afterSeq < 0 || limit < 1 || limit > 1000 {
		return nil, afterSeq, ErrInvalid
	}
	rows, err := q.QueryContext(ctx, `SELECT r.event_id,r.chat_id,r.operation_id,r.run_id,r.trigger_message_id,r.chat_seq,r.created_at FROM chat_entity_refs r JOIN agent_operations o ON o.id=r.operation_id JOIN chats c ON c.id=r.chat_id AND c.workspace_id=o.workspace_id WHERE r.chat_id=? AND r.chat_seq>? AND o.status='completed' ORDER BY r.chat_seq LIMIT ?`, chatID, afterSeq, limit)
	if err != nil {
		return nil, afterSeq, err
	}
	refs := make([]Ref, 0)
	for rows.Next() {
		var r Ref
		if err = rows.Scan(&r.EventID, &r.ChatID, &r.OperationID, &r.RunID, &r.TriggerMessageID, &r.Seq, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, afterSeq, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, afterSeq, err
	}
	// Close the cursor before operation queries: a transaction may own only one
	// connection, and this also avoids exhausting a single-connection test pool.
	var ws string
	if len(refs) > 0 {
		if err = q.QueryRowContext(ctx, `SELECT workspace_id FROM chats WHERE id=?`, chatID).Scan(&ws); err != nil {
			return nil, afterSeq, err
		}
	}
	for i := range refs {
		o, e := GetOperation(ctx, q, ws, refs[i].OperationID)
		if e != nil {
			return nil, afterSeq, fmt.Errorf("load reference operation: %w", e)
		}
		refs[i].Operation = *o
		afterSeq = refs[i].Seq
	}
	return refs, afterSeq, nil
}

// FindOperation looks up a completed result within the caller's actor scope.
// Authorization and checking the stored request hash and run remain the
// caller's responsibility, before this result is returned to a transport.
func FindOperation(ctx context.Context, q Queryer, workspaceID, actorType, actorID, key string) (*Operation, error) {
	return scanOperation(q.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM agent_operations WHERE workspace_id=? AND actor_type=? AND actor_id=? AND operation_key=? AND status='completed'`, workspaceID, actorType, actorID, key))
}
