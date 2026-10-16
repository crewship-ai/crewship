package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/safepath"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Required Slides images are a hard restore boundary, unlike historical optional
// attachment warnings. Runs after attachment publication but BEFORE DB commit;
// a missing/conflicting/corrupt image cannot produce a successful broken deck.
func validateRestoredSlideImageBlobs(ctx context.Context, tx *sql.Tx, root string, dump *DBDump) error {
	keys := map[slides.Key]bool{}
	for _, row := range dump.Tables["slides_revisions"] {
		workspace, wok := row["workspace_id"].(string)
		page, pok := row["page_id"].(string)
		if wok && pok {
			keys[slides.Key{WorkspaceID: workspace, PageID: page}] = true
		}
	}
	return validateSlideImageBlobsForKeys(ctx, tx, root, keys)
}

func validateSlideImageBlobsForKeys(ctx context.Context, tx *sql.Tx, root string, keys map[slides.Key]bool) error {
	for key := range keys {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.sha256,a.byte_size,a.content_type,a.width,a.height FROM slides_revision_assets r JOIN page_assets a ON a.page_id=r.page_id AND a.id=r.asset_id AND a.sha256=r.expected_sha256 WHERE r.workspace_id=? AND r.page_id=?`, key.WorkspaceID, key.PageID)
		if err != nil {
			return err
		}
		type blob struct {
			sha           string
			size          int64
			mime          string
			width, height int
		}
		blobs := []blob{}
		for rows.Next() {
			var b blob
			if err = rows.Scan(&b.sha, &b.size, &b.mime, &b.width, &b.height); err != nil {
				rows.Close()
				return err
			}
			blobs = append(blobs, b)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, b := range blobs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if root == "" || len(b.sha) != 64 || b.size < 1 || b.size > pageassets.MaxOutput {
				return fmt.Errorf("backup: required Slides image storage unavailable")
			}
			path, err := safepath.JoinUnder(root, "attachments", key.WorkspaceID, b.sha[:2], b.sha)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("backup: required Slides image missing: %w", err)
			}
			data, err := io.ReadAll(io.LimitReader(f, pageassets.MaxOutput+1))
			closeErr := f.Close()
			digest := sha256.Sum256(data)
			if err != nil || closeErr != nil || int64(len(data)) != b.size || hex.EncodeToString(digest[:]) != b.sha {
				return fmt.Errorf("backup: required Slides image bytes corrupt or conflicting")
			}
			metadata, err := pageassets.ValidateCanonical(data)
			if err != nil || metadata.MIME != b.mime || metadata.Width != b.width || metadata.Height != b.height {
				return fmt.Errorf("backup: required Slides image actual format differs from metadata")
			}
		}
	}
	return nil
}

// Offline physical recovery copies a whole DB instead of using RestoreDumpTx.
// Verify all retained native image roots/bytes before reporting recovery success.
func validateRecoveredSlideImages(ctx context.Context, db *sql.DB, root string) error {
	var present int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='slides_heads'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id,page_id FROM slides_heads LIMIT 129`)
	if err != nil {
		return err
	}
	keys := map[slides.Key]bool{}
	for rows.Next() {
		var key slides.Key
		if err = rows.Scan(&key.WorkspaceID, &key.PageID); err != nil {
			rows.Close()
			return err
		}
		keys[key] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(keys) > 128 {
		return fmt.Errorf("backup: recovered Slides exceed instance deck budget")
	}
	for key := range keys {
		if err := slides.ValidateImagePageTx(ctx, tx, key); err != nil {
			return fmt.Errorf("backup: recovered Slides image roots: %w", err)
		}
	}
	return validateSlideImageBlobsForKeys(ctx, tx, root, keys)
}
