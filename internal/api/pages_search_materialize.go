package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/pagesearch"
	"github.com/crewship-ai/crewship/internal/slides"
)

var errNativeSearchUnavailable = errors.New("supported native content search is not ready; retry")

// ensureNativeSearchProjection is viewer-triggered. It carries only the current
// request bearer to existing private IPC; there is no background human identity.
// Callers supply the overall search deadline, covering waiting, IPC and final CAS.
func (h *PageHandler) ensureNativeSearchProjection(ctx context.Context, r *http.Request, pageID string) error {
	_, _, ready, err := h.nativeSearchCachedSource(ctx, r, pageID, false)
	if err != nil || ready {
		return err
	}
	h.initNativeSearch()
	select {
	case h.nativeSearchSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.nativeSearchSlots }()
	// A preceding rebuild may already have published. Recheck this viewer's live
	// authority and current source after waiting, rather than reusing old admission.
	source, canonical, ready, err := h.nativeSearchCachedSource(ctx, r, pageID, true)
	if err != nil || ready {
		return err
	}
	var projection pagesearch.Projection
	switch source.Type {
	case "slides":
		projection, err = pagesearch.FromSlides(source, canonical)
	case "document":
		if h.documentCall == nil {
			return errNativeSearchUnavailable
		}
		token := extractToken(r)
		if token == "" {
			return errNativeSearchAuthentication
		}
		raw, status, callErr := h.documentCall(ctx, source.WorkspaceID, "Bearer "+token, pageID, "read", map[string]any{})
		if callErr != nil {
			return errNativeSearchUnavailable
		}
		if status == 401 {
			return errNativeSearchAuthentication
		}
		if status == 403 {
			return errNativeSearchMembership
		}
		if status != 200 {
			return errNativeSearchUnavailable
		}
		projection, err = materializedDocumentProjection(source, raw)
	default:
		return pagesearch.ErrUnsupported
	}
	if err != nil {
		return err
	}
	return pagesearch.NewStore(h.db).Publish(ctx, projection, h.nativeSearchGuard(r, pageID))
}

func (h *PageHandler) nativeSearchCachedSource(ctx context.Context, r *http.Request, pageID string, canonicalNeeded bool) (pagesearch.SourceTag, []byte, bool, error) {
	var source pagesearch.SourceTag
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source, nil, false, err
	}
	defer tx.Rollback()
	guard := h.nativeSearchGuard(r, pageID)
	source, err = guard(ctx, tx)
	if err != nil {
		return source, nil, false, err
	}
	_, err = pagesearch.NewStore(h.db).Load(ctx, tx, source, guard)
	if err == nil {
		return source, nil, true, nil
	}
	if !errors.Is(err, pagesearch.ErrNotFound) && !errors.Is(err, pagesearch.ErrSourceChanged) && !errors.Is(err, pagesearch.ErrInvalid) && !errors.Is(err, pagesearch.ErrUnsupported) && !errors.Is(err, pagesearch.ErrBudget) {
		return source, nil, false, err
	}
	var canonical []byte
	if canonicalNeeded && source.Type == "slides" {
		var size int
		err = tx.QueryRowContext(ctx, `SELECT length(CAST(canonical_json AS BLOB)),CASE WHEN length(CAST(canonical_json AS BLOB))<=? THEN CAST(canonical_json AS BLOB) ELSE NULL END
 FROM slides_heads WHERE workspace_id=? AND page_id=?`, slides.MaxDeckBytes, source.WorkspaceID, source.PageID).Scan(&size, &canonical)
		// Both the SQL guard and driver-side check count actual encoded bytes.
		if err != nil {
			return source, nil, false, err
		}
		if len(canonical) < 1 || len(canonical) > slides.MaxDeckBytes || size != len(canonical) || size < 1 {
			return source, nil, false, pagesearch.ErrInvalid
		}
	}
	return source, canonical, false, nil
}

func materializedDocumentProjection(source pagesearch.SourceTag, raw []byte) (pagesearch.Projection, error) {
	var value struct {
		Schema      int                    `json:"schema"`
		Revision    int64                  `json:"rev"`
		Generation  int64                  `json:"generation"`
		Epoch       string                 `json:"epoch"`
		Incarnation string                 `json:"incarnation"`
		Blocks      []pagesearch.BlockHash `json:"blocks"`
		Outline     []struct {
			ID, Type, Hash, Text string
			Parent               *string
		} `json:"outline"`
	}
	if len(raw) > 4<<20 {
		return pagesearch.Projection{}, pagesearch.ErrBudget
	}
	if !utf8.Valid(raw) {
		return pagesearch.Projection{}, pagesearch.ErrInvalid
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return pagesearch.Projection{}, pagesearch.ErrInvalid
	}
	if value.Schema != source.Schema || value.Revision != source.Revision || value.Generation != source.Generation {
		return pagesearch.Projection{}, pagesearch.ErrSourceChanged
	}
	if value.Epoch == "" || len(value.Epoch) > 512 || value.Incarnation == "" || len(value.Incarnation) > 256 || value.Blocks == nil || value.Outline == nil || len(value.Blocks) > pagesearch.MaxSegments || len(value.Outline) > pagesearch.MaxSegments {
		return pagesearch.Projection{}, pagesearch.ErrInvalid
	}
	outline := make([]pagesearch.DocumentBlock, 0, len(value.Outline))
	for _, block := range value.Outline {
		parent := ""
		if block.Parent != nil {
			parent = *block.Parent
		}
		outline = append(outline, pagesearch.DocumentBlock{ID: block.ID, Type: block.Type, Parent: parent, Hash: block.Hash, Text: block.Text})
	}
	return pagesearch.FromDocument(source, outline, value.Blocks)
}
