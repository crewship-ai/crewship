package slides

import (
	"context"
	"database/sql"
	"errors"
)

// ClaimSelectedRequest performs the single queued-to-running transition. A
// retry cannot dispatch again; failed dispatch is settled explicitly instead.
func (s *Store) ClaimSelectedRequest(ctx context.Context, key Key, id string, authorize Authorize) (record SelectedAuthorityState, err error) {
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		var e error
		record, e = ReadSelectedAuthorityTx(ctx, tx, key, id)
		if e != nil {
			return e
		}
		if record.Summary.Restored || record.Summary.Status != "queued" {
			return ErrConflict
		}
		head, e := readState(ctx, tx, key, 0)
		if e != nil {
			return e
		}
		if head.Revision != record.Summary.BaseRevision || head.ContentHash != record.Summary.BaseContentHash {
			return ErrConflict
		}
		if _, e = tx.ExecContext(ctx, `UPDATE slides_requests SET status='running',version=version+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, key.WorkspaceID, key.PageID, id); e != nil {
			return e
		}
		if e = selectedEvent(ctx, tx, key, id, record.Summary.Version+1, "dispatched", actor, "Frozen selection dispatched", ""); e != nil {
			return e
		}
		record, e = ReadSelectedAuthorityTx(ctx, tx, key, id)
		return e
	})
	return
}

// BindSelectedRun is trusted host integration, not an agent operation. The
// callback inserts its narrowly scoped delegation in this same transaction.
func (s *Store) BindSelectedRun(ctx context.Context, key Key, id, runID string, authorize Authorize, bind func(context.Context, *sql.Tx, SelectedAuthorityState) error) error {
	if !identifier.MatchString(runID) || bind == nil {
		return errors.New("trusted selected run binding required")
	}
	return s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		record, err := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		if record.Summary.Restored || record.Summary.Status != "running" {
			return ErrConflict
		}
		if record.Summary.OriginalRunID != "" {
			if record.RunID != runID || record.Summary.OriginalRunID != runID {
				return ErrConflict
			}
			return bind(ctx, tx, record)
		}
		head, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if head.Revision != record.Summary.BaseRevision || head.ContentHash != record.Summary.BaseContentHash {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_requests SET run_id=?,original_run_id=?,version=version+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, runID, runID, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		if err = selectedEvent(ctx, tx, key, id, record.Summary.Version+1, "bound", actor, "Original run bound", ""); err != nil {
			return err
		}
		record, err = ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		return bind(ctx, tx, record)
	})
}

// SelectedResultGuard pins the one request-owned result inside PatchWithReceipt.
// Authorization must independently validate live credentials/run/delegation on
// every invocation, including replay. This guard never refreshes the selection.
func SelectedResultGuard(key Key, id, runID string) PatchReceiptGuard {
	return func(ctx context.Context, tx *sql.Tx, actual Key, actor string, receipt Receipt) error {
		if actual != key || !identifier.MatchString(runID) {
			return ErrConflict
		}
		record, err := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		r := record.Summary
		if r.Restored || r.OriginalRunID != runID || actor != r.AgentActor+":"+runID || receipt.MutationID != record.OriginRequestID+"-patch" || receipt.Revision != r.BaseRevision+1 {
			return ErrConflict
		}
		if r.ResultRevision != nil {
			if (r.Status != "running" && r.Status != "succeeded") || *r.ResultRevision != receipt.Revision || r.ResultHash != receipt.ContentHash {
				return ErrConflict
			}
			return nil
		}
		if r.Status != "running" || record.RunID != runID {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_requests SET result_revision=?,result_hash=?,version=version+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, receipt.Revision, receipt.ContentHash, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		if err = selectedEvent(ctx, tx, key, id, r.Version+1, "result", actor, "Immutable replacement revision retained", ""); err != nil {
			return err
		}
		_, err = ReadSelectedAuthorityTx(ctx, tx, key, id)
		return err
	}
}

// SettleSelectedRequest records a trusted terminal outcome without creating a
// content checkpoint. Repeated settlement is idempotent and does not add events.
// Failure detail must be a fixed host-authored message, never provider output.
func (s *Store) SettleSelectedRequest(ctx context.Context, key Key, id, status, detail string, authorize Authorize) error {
	if (status != "succeeded" && status != "failed" && status != "interrupted") || !validText(detail, MaxSelectedRequestErrorBytes) {
		return ErrInvalid
	}
	return s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		record, err := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		r := record.Summary
		if !selectedActive(r.Status) {
			if r.Status == status {
				return nil
			}
			return ErrConflict
		}
		if r.Restored || (status == "succeeded" && (r.Status != "running" || r.ResultRevision == nil)) {
			return ErrConflict
		}
		// Normal success retains safe original admission identities for authorized
		// result-receipt replay. Failed/interrupted work cannot regain capability.
		clear := status != "succeeded"
		if _, err = tx.ExecContext(ctx, `UPDATE slides_requests SET status=?,error=?,version=version+1,run_id=NULL,audience_json=CASE WHEN ? THEN '{}' ELSE audience_json END,credential_id=CASE WHEN ? THEN '' ELSE credential_id END,credential_hash=CASE WHEN ? THEN '' ELSE credential_hash END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, status, detail, clear, clear, clear, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		return selectedEvent(ctx, tx, key, id, r.Version+1, status, actor, detail, "")
	})
}
