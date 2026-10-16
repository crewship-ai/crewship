package slides

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// StoredSelectedBinding is trusted backup integration metadata, not a public
// wire protocol. Decode retains its own stored-JSON budgets and exact keys.
type StoredSelectedBinding struct {
	BaseRevision      int64
	BaseSchemaVersion int
	BaseContentHash   string
	Fingerprint       string
	InputJSON         string
	VerifiedJSON      string
}

func SelectedBindingPageID(binding StoredSelectedBinding) (string, error) {
	input, err := decodeStoredSelectedInput([]byte(binding.InputJSON))
	return input.PageID, err
}

func DecodeStoredSelectedBinding(key Key, binding StoredSelectedBinding) (SelectedRequestInput, VerifiedTextSelection, error) {
	input, err := decodeStoredSelectedInput([]byte(binding.InputJSON))
	if err != nil {
		return input, VerifiedTextSelection{}, err
	}
	verified, err := decodeVerifiedTextSelection([]byte(binding.VerifiedJSON))
	if err != nil {
		return input, verified, err
	}
	fingerprint, err := requestHash(key, "selected/admit", input)
	if err != nil || fingerprint != binding.Fingerprint || input.PageID != key.PageID || input.BaseRev != binding.BaseRevision || binding.BaseSchemaVersion != verified.DeckSchemaVersion || binding.BaseContentHash != verified.ContentHash || input.Selection != verified.Selection {
		return input, verified, errors.New("invalid stored selected binding")
	}
	return input, verified, nil
}

// RemapStoredSelectedBinding never hashes a deck. The sole canonical backup
// rewrite supplies the hash of the already remapped immutable base revision.
// Original text/selection IDs and historical chat identity remain provenance.
func RemapStoredSelectedBinding(oldKey, newKey Key, binding StoredSelectedBinding, newBaseHash string) (StoredSelectedBinding, error) {
	input, verified, err := DecodeStoredSelectedBinding(oldKey, binding)
	if err != nil || !hashPattern.MatchString(newBaseHash) || !identifier.MatchString(newKey.PageID) || newKey.WorkspaceID == "" {
		return binding, errors.New("invalid selected fork binding")
	}
	input.PageID = newKey.PageID
	verified.ContentHash = newBaseHash
	inputJSON, err := canonicalValue(input)
	if err != nil {
		return binding, err
	}
	verifiedJSON, err := canonicalValue(verified)
	if err != nil {
		return binding, err
	}
	binding.InputJSON, binding.VerifiedJSON, binding.BaseContentHash = string(inputJSON), string(verifiedJSON), newBaseHash
	binding.Fingerprint, err = requestHash(newKey, "selected/admit", input)
	return binding, err
}

const SelectedRestoreMessage = "Agent work suspended by backup restore; retained history is inert. Submit a new request after review"

// FenceSelectedRestorePageTx reduces only imported request identities to inert
// history, including INSERT OR IGNORE collisions with existing target requests.
// Every immutable original/result binding and complete event sequence is checked
// in this restore transaction. No imported credential or delegation can survive.
func FenceSelectedRestorePageTx(ctx context.Context, tx *sql.Tx, key Key, ids []string) error {
	if len(ids) > MaxSelectedRetainedRequests {
		return ErrQuota
	}
	for _, id := range ids {
		record, err := ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		r := record.Summary
		if selectedActive(r.Status) {
			if r.Version >= MaxSelectedRequestEvents {
				return ErrQuota
			}
			// A prepared source event may have landed while the existing request
			// row was ignored. Accept only the resulting validated full history.
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO slides_request_events(workspace_id,page_id,request_id,version,kind,actor,detail) VALUES(?,?,?,?,'interrupted','system:backup-restore',?)`, key.WorkspaceID, key.PageID, id, r.Version+1, SelectedRestoreMessage); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE slides_requests SET status='interrupted',version=version+1,error=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE workspace_id=? AND page_id=? AND id=?`, SelectedRestoreMessage, key.WorkspaceID, key.PageID, id); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE slides_requests SET restored=1,run_id=NULL,human_user_id=NULL,agent_id=NULL,chat_id=NULL,audience_json='{}',credential_id='',credential_hash='' WHERE workspace_id=? AND page_id=? AND id=?`, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM slides_request_receipts WHERE workspace_id=? AND page_id=? AND request_id=?`, key.WorkspaceID, key.PageID, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM page_artifact_run_delegations WHERE page_id=? AND slides_request_id=? AND page_id IN (SELECT id FROM pages WHERE workspace_id=?)`, key.PageID, id, key.WorkspaceID); err != nil {
			return err
		}
		record, err = ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return err
		}
		if _, err = selectedHistory(ctx, tx, key, record); err != nil {
			return err
		}
		if _, err = time.Parse(time.RFC3339Nano, record.Summary.CreatedAt); err != nil { // tsformat:allow: retained timestamp syntax only; history orders by integer version
			return err
		}
		if _, err = time.Parse(time.RFC3339Nano, record.Summary.UpdatedAt); err != nil { // tsformat:allow: retained timestamp syntax only; no timestamp is written
			return err
		}
	}
	var total, bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(CAST(input_json AS BLOB))+length(CAST(verified_json AS BLOB))+length(CAST(audience_json AS BLOB))),0) FROM slides_requests WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&total, &bytes); err != nil {
		return err
	}
	if total > MaxSelectedRetainedRequests || bytes > MaxSelectedRequestHistoryBytes {
		return ErrQuota
	}
	return nil
}
