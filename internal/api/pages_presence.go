package api

import (
	"context"
	"database/sql"
	"errors"
)

// CanReadPageContent is the live human ACL for dedicated private WS rooms.
// The WS transport derives userID from its authenticated ticket, never payload.
func (h *PageHandler) CanReadPageContent(ctx context.Context, userID, kind, pageID string) (bool, error) {
	if userID == "" || (kind != "artifact" && kind != "board") {
		return false, nil
	}
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	var ws, slug, role string
	err = tx.QueryRowContext(ctx, `SELECT p.workspace_id,p.slug,m.role FROM pages p JOIN workspace_members m ON m.workspace_id=p.workspace_id AND m.user_id=? WHERE p.id=? AND p.owner_user_id IS NOT NULL AND p.owner_crew_id IS NULL`, userID, pageID).Scan(&ws, &slug, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, err := h.loadPage(ctx, ws, slug)
	if err != nil {
		return false, err
	}
	if (kind == "artifact" && !rec.HasArtifact) || (kind == "board" && !rec.HasBoard) {
		return false, nil
	}
	viewer, err := h.loadViewer(ctx, ws, userID)
	if err != nil {
		return false, err
	}
	return h.canSeePage(ctx, ws, rec, nil, viewer)
}
