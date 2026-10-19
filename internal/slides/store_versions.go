package slides

import (
	"context"
	"database/sql"
	"encoding/json"
)

// These operations retain the common writer fence and both live authority
// checks. A returned receipt is publishable only when the operation succeeds;
// restore appends a revision and cannot bypass real image attachment hooks.
func (s *Store) Reorder(ctx context.Context, key Key, request ReorderRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeReorder(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "reorder", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "reorder")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.SchemaVersion != request.SchemaVersion || current.Revision != request.BaseRev || current.OrderVersion != request.IfOrderVersion {
			return ErrConflict
		}
		candidate := current.Deck
		candidate.SlideOrder = append([]string{}, request.SlideOrder...)
		result, err = s.write(ctx, tx, key, current, candidate, request.MutationID, "reorder", hash, actor, nil, false)
		return err
	})
	return
}
func (s *Store) Restore(ctx context.Context, key Key, request RestoreRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeRestore(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "restore", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "restore")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.SchemaVersion != request.SchemaVersion || current.Revision != request.BaseRev || current.OrderVersion != request.IfOrderVersion {
			return ErrConflict
		}
		target, err := readState(ctx, tx, key, request.Revision)
		if err != nil {
			return err
		}
		result, err = s.write(ctx, tx, key, current, target.Deck, request.MutationID, "restore", hash, actor, ObjectIDs(target.Deck), true)
		return err
	})
	return
}
func (s *Store) History(ctx context.Context, key Key, authorize Authorize) (history []Revision, err error) {
	history = []Revision{}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		if _, err := readState(ctx, tx, key, 0); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT rev,parent_rev,order_version,schema_version,content_hash,actor,kind,mutation_id,created_at FROM slides_revisions WHERE workspace_id=? AND page_id=? ORDER BY rev DESC LIMIT ?`, key.WorkspaceID, key.PageID, s.options.MaxRevisions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var revision Revision
			if err = rows.Scan(&revision.Revision, &revision.ParentRevision, &revision.OrderVersion, &revision.SchemaVersion, &revision.ContentHash, &revision.Actor, &revision.Kind, &revision.MutationID, &revision.CreatedAt); err != nil {
				return err
			}
			history = append(history, revision)
		}
		return rows.Err()
	})
	// Never return collected protected metadata after final authorization or commit fails.
	if err != nil {
		history = nil
	}
	return
}
