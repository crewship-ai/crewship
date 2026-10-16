package slides

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type CancelSelectedRequestInput struct {
	PageID        string `json:"page_id"`
	SchemaVersion int    `json:"schema_version"`
	MutationID    string `json:"mutation_id"`
	IfVersion     int64  `json:"if_version"`
}
type SelectedMutationReceipt struct {
	MutationID string `json:"mutation_id"`
	RequestID  string `json:"request_id"`
	Version    int64  `json:"version"`
	Status     string `json:"status"`
	Duplicate  bool   `json:"duplicate"`
}

func validateSelectedCancel(request CancelSelectedRequestInput) error {
	if request.SchemaVersion != SelectedRequestProtocolVersion || !identifier.MatchString(request.PageID) || !identifier.MatchString(request.MutationID) || request.IfVersion < 1 {
		return errors.New("invalid selected request cancellation")
	}
	return nil
}
func DecodeSelectedCancel(raw []byte) (CancelSelectedRequestInput, error) {
	var request CancelSelectedRequestInput
	if len(raw) > MaxSelectedRequestBytes {
		return request, errors.New("cancellation exceeds16KiB")
	}
	if err := strictRequest(raw, &request, nil, []string{"page_id", "schema_version", "mutation_id", "if_version"}, nil); err != nil {
		return request, err
	}
	return request, validateSelectedCancel(request)
}
func (s *Store) CancelSelectedRequest(ctx context.Context, key Key, id string, request CancelSelectedRequestInput, authorize Authorize) (result SelectedMutationReceipt, err error) {
	if validateSelectedCancel(request) != nil || !identifier.MatchString(id) {
		return result, fmt.Errorf("%w: invalid selected cancellation", ErrInvalid)
	}
	hash, err := requestHash(key, "selected/cancel/"+id, request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		record, e := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if e != nil {
			return e
		}
		if record.Summary.Restored {
			return ErrDuplicate
		}
		var previousID, previousActor, previousHash string
		var version int64
		e = tx.QueryRowContext(ctx, `SELECT request_id,event_version,actor,request_hash FROM slides_request_receipts WHERE workspace_id=? AND page_id=? AND mutation_id=?`, key.WorkspaceID, key.PageID, request.MutationID).Scan(&previousID, &version, &previousActor, &previousHash)
		if e == nil {
			if previousID != id || previousActor != actor || previousHash != hash {
				return ErrDuplicate
			}
			result = SelectedMutationReceipt{MutationID: request.MutationID, RequestID: id, Version: version, Status: "cancelled", Duplicate: true}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var historic bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM slides_request_events WHERE workspace_id=? AND page_id=? AND mutation_id=?)`, key.WorkspaceID, key.PageID, request.MutationID).Scan(&historic); e != nil {
			return e
		}
		if historic {
			return ErrDuplicate
		}
		r := record.Summary
		if !selectedActive(r.Status) || r.Version != request.IfVersion {
			return ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE slides_requests SET status='cancelled',version=version+1,run_id=NULL,audience_json='{}',credential_id='',credential_hash='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, key.WorkspaceID, key.PageID, id)
		if e != nil {
			return e
		}
		if e = selectedEvent(ctx, tx, key, id, r.Version+1, "cancelled", actor, "Explicit human cancellation", request.MutationID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO slides_request_receipts(workspace_id,page_id,mutation_id,request_id,event_version,actor,request_hash) VALUES(?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, request.MutationID, id, r.Version+1, actor, hash)
		result = SelectedMutationReceipt{MutationID: request.MutationID, RequestID: id, Version: r.Version + 1, Status: "cancelled"}
		return e
	})
	return
}
