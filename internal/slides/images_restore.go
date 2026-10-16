package slides

import (
	"context"
	"database/sql"
	"errors"
)

// ValidateImagePageTx checks immutable canonical references against their exact
// retained asset pins. Backup runs this after all rows are inserted, before any
// commit or external restore hook. No bytes are rewritten and no refs invented.
func ValidateImagePageTx(ctx context.Context, tx *sql.Tx, key Key) error {
	head, err := readState(ctx, tx, key, 0)
	if err != nil {
		return err
	}
	pinnedHead, err := readState(ctx, tx, key, head.Revision)
	if err != nil || pinnedHead.ContentHash != head.ContentHash || pinnedHead.SchemaVersion != head.SchemaVersion {
		return errors.New("Slides head differs from its immutable revision")
	}
	rows, err := tx.QueryContext(ctx, `SELECT rev FROM slides_revisions WHERE workspace_id=? AND page_id=? ORDER BY rev LIMIT 1001`, key.WorkspaceID, key.PageID)
	if err != nil {
		return err
	}
	revisions := []int64{}
	for rows.Next() {
		var rev int64
		if err = rows.Scan(&rev); err != nil {
			rows.Close()
			return err
		}
		revisions = append(revisions, rev)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(revisions) > 1000 {
		return ErrQuota
	}
	for _, revision := range revisions {
		state, err := readState(ctx, tx, key, revision)
		if err != nil {
			return err
		}
		expected := ImageReferences(state.Deck)
		pins, err := tx.QueryContext(ctx, `SELECT p.asset_id,p.expected_sha256,a.sha256,a.state,a.byte_size FROM slides_revision_assets p JOIN page_assets a ON a.page_id=p.page_id AND a.id=p.asset_id WHERE p.workspace_id=? AND p.page_id=? AND p.rev=?`, key.WorkspaceID, key.PageID, revision)
		if err != nil {
			return err
		}
		count, total := 0, int64(0)
		for pins.Next() {
			var id, want, sha, status string
			var size int64
			if err = pins.Scan(&id, &want, &sha, &status, &size); err != nil {
				pins.Close()
				return err
			}
			count++
			total += size
			if expected[id] != want || sha != want || status != "retained" || size < 1 || total > MaxImageAssetBytes {
				pins.Close()
				return errors.New("Slides retained image root mismatch")
			}
		}
		if err = pins.Err(); err != nil {
			pins.Close()
			return err
		}
		pins.Close()
		if count != len(expected) {
			return errors.New("Slides immutable image root set differs from canonical revision")
		}
	}
	return nil
}
