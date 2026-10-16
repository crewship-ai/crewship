package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/pagesearch"
)

// nativeSearchGuard retains only this request's existing human context. Every
// invocation rechecks it and the actual source inside the supplied transaction;
// neither previous candidate authorization nor managed IPC grants authority.
func (h *PageHandler) nativeSearchGuard(r *http.Request, pageID string) pagesearch.Guard {
	return func(ctx context.Context, tx *sql.Tx) (pagesearch.SourceTag, error) {
		ctx, viewer, err := h.nativeSearchViewer(ctx, r, tx)
		if err != nil {
			return pagesearch.SourceTag{}, err
		}
		workspace := WorkspaceIDFromContext(ctx)
		var page pageRecord
		var document, slide bool
		err = tx.QueryRowContext(ctx, `SELECT p.id,p.slug,COALESCE(p.owner_user_id,''),COALESCE(p.owner_crew_id,''),COALESCE(p.folder_id,''),
 EXISTS(SELECT 1 FROM documents WHERE workspace_id=p.workspace_id AND page_id=p.id),
 EXISTS(SELECT 1 FROM slides_heads WHERE workspace_id=p.workspace_id AND page_id=p.id)
 FROM pages p WHERE p.workspace_id=? AND p.id=?`, workspace, pageID).Scan(&page.ID, &page.Slug, &page.OwnerUserID, &page.OwnerCrewID, &page.FolderID, &document, &slide)
		if errors.Is(err, sql.ErrNoRows) {
			return pagesearch.SourceTag{}, pagesearch.ErrSourceChanged
		}
		if err != nil {
			return pagesearch.SourceTag{}, err
		}
		panels, err := h.assetPagePanels(ctx, page.ID)
		if err != nil {
			return pagesearch.SourceTag{}, err
		}
		visible, err := h.canSeePage(ctx, workspace, &page, panels, viewer)
		if err != nil {
			return pagesearch.SourceTag{}, err
		}
		if !visible {
			return pagesearch.SourceTag{}, pagesearch.ErrSourceChanged
		}
		if document == slide {
			return pagesearch.SourceTag{}, pagesearch.ErrUnsupported
		}
		tag := pagesearch.SourceTag{WorkspaceID: workspace, PageID: page.ID}
		if document {
			tag.Type = "document"
			var raw []byte
			var size int
			err = tx.QueryRowContext(ctx, `SELECT schema_version,head_rev,generation,length(projection),
 CASE WHEN length(projection)<=? THEN projection ELSE NULL END FROM documents WHERE workspace_id=? AND page_id=?`,
				pagesearch.MaxProjectionBytes, workspace, page.ID).Scan(&tag.Schema, &tag.Revision, &tag.Generation, &size, &raw)
			if err != nil {
				return pagesearch.SourceTag{}, err
			}
			if size != len(raw) || size > pagesearch.MaxProjectionBytes || !utf8.Valid(raw) {
				return pagesearch.SourceTag{}, pagesearch.ErrInvalid
			}
			var blocks []pagesearch.BlockHash
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&blocks); err != nil {
				return pagesearch.SourceTag{}, pagesearch.ErrInvalid
			}
			if err = decoder.Decode(new(any)); err != io.EOF {
				return pagesearch.SourceTag{}, pagesearch.ErrInvalid
			}
			if blocks == nil {
				return pagesearch.SourceTag{}, pagesearch.ErrInvalid
			}
			tag.Hash, err = pagesearch.DocumentHash(blocks)
			if err != nil {
				return pagesearch.SourceTag{}, err
			}
		} else {
			tag.Type = "slides"
			// A head without its exact retained immutable snapshot/hash is unavailable,
			// not a current source from which cached text can be returned.
			err = tx.QueryRowContext(ctx, `SELECT h.schema_version,h.head_rev,h.content_hash
 FROM slides_heads h JOIN slides_revisions r ON r.workspace_id=h.workspace_id AND r.page_id=h.page_id AND r.rev=h.head_rev
 WHERE h.workspace_id=? AND h.page_id=? AND h.schema_version=r.schema_version AND h.content_hash=r.content_hash AND h.canonical_json=r.canonical_json`, workspace, page.ID).Scan(&tag.Schema, &tag.Revision, &tag.Hash)
			if errors.Is(err, sql.ErrNoRows) {
				return pagesearch.SourceTag{}, pagesearch.ErrSourceChanged
			}
			if err != nil {
				return pagesearch.SourceTag{}, err
			}
		}
		return tag, tag.Validate()
	}
}
