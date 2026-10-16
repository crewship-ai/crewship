package slides

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/pageassets"
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
	var pinsPresent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='slides_revision_assets'`).Scan(&pinsPresent); err != nil {
		return err
	}
	for _, revision := range revisions {
		state, err := readState(ctx, tx, key, revision)
		if err != nil {
			return err
		}
		expected := ImageReferences(state.Deck)
		if pinsPresent == 0 {
			// Older no-kit physical recovery may leave the additive migration
			// pending. Only exact closed v1 snapshots need no image root table.
			if state.SchemaVersion != SchemaVersion || len(expected) != 0 {
				return errors.New("Slides image schema requires immutable asset pins")
			}
			continue
		}
		pins, err := tx.QueryContext(ctx, `SELECT p.asset_id,p.expected_sha256,a.sha256,a.state,a.byte_size,a.width,a.height FROM slides_revision_assets p JOIN page_assets a ON a.page_id=p.page_id AND a.id=p.asset_id WHERE p.workspace_id=? AND p.page_id=? AND p.rev=? LIMIT ?`, key.WorkspaceID, key.PageID, revision, MaxImageAssets+1)
		if err != nil {
			return err
		}
		count, total, totalPixels := 0, int64(0), int64(0)
		for pins.Next() {
			var id, want, sha, status string
			var size, width, height int64
			if err = pins.Scan(&id, &want, &sha, &status, &size, &width, &height); err != nil {
				pins.Close()
				return err
			}
			if expected[id] != want || sha != want || status != "retained" || size < 1 || size > pageassets.MaxOutput || size > MaxImageAssetBytes-total || width < 1 || height < 1 || width > pageassets.MaxDimension || height > pageassets.MaxDimension {
				pins.Close()
				return errors.New("Slides retained image root mismatch")
			}
			pixels := width * height
			if pixels > pageassets.MaxPixels || pixels > MaxImageAssetPixels-totalPixels {
				pins.Close()
				return errors.New("Slides retained image pixel budget exceeded")
			}
			totalPixels += pixels
			total += size
			count++
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
