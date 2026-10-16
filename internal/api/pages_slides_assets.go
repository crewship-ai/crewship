package api

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// newSlidesStore is the single human/selected-agent production constructor.
// Every durable revision, including selected edits that preserve image refs,
// must execute the same Page asset validation and pinning transaction hook.
func (h *PageHandler) newSlidesStore() *slides.Store {
	return slides.New(h.db, slides.Options{OnRevision: h.pinSlideAssets})
}

// pinSlideAssets runs after the immutable revision INSERT and before receipt and
// final live credential/Page authorization. Never acquire a blob mutex here:
// uploads and collection acquire blob lock before DB writer admission. Every live
// asset row is already a shared blob-GC root; the writer transaction serializes
// staged expiry, so a valid row protects bytes while they are verified/pinned.
func (h *PageHandler) pinSlideAssets(ctx context.Context, tx *sql.Tx, key slides.Key, revision int64, deck slides.Deck) error {
	refs := slides.ImageReferences(deck)
	if len(refs) > slides.MaxImageAssets {
		return fmt.Errorf("%w: too many image assets", slides.ErrInvalid)
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := tsformat.Format(time.Now().UTC())
	total := int64(0)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		a, err := readPageAssetMetadata(ctx, tx, key.PageID, id, now)
		if err != nil {
			return fmt.Errorf("%w: image asset unavailable", slides.ErrInvalid)
		}
		// The canonical Deck and production authority bind PageID to the workspace;
		// verify it again here so the hook is safe for every production caller.
		var workspace string
		if err = tx.QueryRowContext(ctx, `SELECT workspace_id FROM pages WHERE id=?`, key.PageID).Scan(&workspace); err != nil || workspace != key.WorkspaceID {
			return fmt.Errorf("%w: image Page identity mismatch", slides.ErrInvalid)
		}
		if a.SHA256 != refs[id] || (a.ContentType != "image/png" && a.ContentType != "image/jpeg") || a.Width < 1 || a.Width > pageassets.MaxDimension || a.Height < 1 || a.Height > pageassets.MaxDimension || a.Width > pageassets.MaxPixels/a.Height || a.ByteSize < 1 || a.ByteSize > pageassets.MaxOutput || a.ByteSize > int64(slides.MaxImageAssetBytes)-total {
			return fmt.Errorf("%w: image identity or budget refused", slides.ErrInvalid)
		}
		total += a.ByteSize
		// One bounded canonical blob at a time, not an all-assets allocation. The
		// digest and exact length detect missing/truncated/corrupt storage bytes.
		data, err := readPageAssetBlob(h.artifactStoragePath, key.WorkspaceID, a.SHA256)
		if err != nil || int64(len(data)) != a.ByteSize || attachmentDigest(data) != a.SHA256 {
			return fmt.Errorf("%w: image bytes unavailable", slides.ErrInvalid)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE page_assets SET state='retained' WHERE page_id=? AND id=?`, key.PageID, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO slides_revision_assets(workspace_id,page_id,rev,asset_id,expected_sha256) VALUES(?,?,?,?,?)`, key.WorkspaceID, key.PageID, revision, id, a.SHA256); err != nil {
			return err
		}
	}
	return nil
}
