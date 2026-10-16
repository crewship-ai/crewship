package slides

import (
	"context"
	"database/sql"
)

type SelectedSnapshot struct {
	Request         SelectedRequestSummary `json:"request"`
	OriginalElement Element                `json:"original_element"`
}

// SelectedSnapshot returns only the frozen selected element, never a newer
// full deck or refreshed preconditions. Its caller supplies live run authority.
func (s *Store) SelectedSnapshot(ctx context.Context, key Key, id string, authorize Authorize) (result SelectedSnapshot, err error) {
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		record, e := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if e != nil {
			return e
		}
		result = SelectedSnapshot{Request: record.Summary, OriginalElement: record.Verified.OriginalElement}
		return nil
	})
	return
}

// ReplaceSelectedText admits no caller-supplied target, hash, revision or style.
// The first fence reconstructs the patch from the immutable pinned snapshot;
// PatchWithReceipt then rechecks live authority in its own writer transaction,
// applies original CAS and atomically binds the one owned result.
func (s *Store) ReplaceSelectedText(ctx context.Context, key Key, id, runID, replacement string, authorize Authorize) (Receipt, error) {
	var patch PatchRequest
	err := s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		record, err := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		pinned, err := readState(ctx, tx, key, record.Summary.BaseRevision)
		if err != nil {
			return err
		}
		patch, err = SelectedReplacementPatch(pinned, key.PageID, record.OriginRequestID, record.Summary.BaseRevision, record.Verified, replacement)
		return err
	})
	if err != nil {
		return Receipt{}, err
	}
	return s.PatchWithReceipt(ctx, key, patch, authorize, SelectedResultGuard(key, id, runID))
}
