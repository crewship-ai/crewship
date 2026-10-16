package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const maxNativeSearchCandidates = 256

var (
	errNativeSearchAuthentication = errors.New("current human pages:read credential required")
	errNativeSearchMembership     = errors.New("current trusted workspace membership required")
	errNativeSearchCandidates     = errors.New("native content search exceeds authorized Page limit")
)

type nativeSearchCandidate struct {
	PageID   string
	Slug     string
	Document bool
	Slides   bool
}

// nativeSearchCandidates runs only inside the caller's read transaction. It
// streams headers without retaining hidden rows or reading canonical text,
// projections or panel payload. Existing Page reach remains the sole policy.
func (h *PageHandler) nativeSearchCandidates(r *http.Request, tx *sql.Tx) ([]nativeSearchCandidate, error) {
	if tx == nil {
		return nil, errNativeSearchAuthentication
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ctx, viewer, err := h.nativeSearchViewer(ctx, r, tx)
	if err != nil {
		return nil, err
	}
	workspace := WorkspaceIDFromContext(ctx)
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.slug,COALESCE(p.owner_user_id,''),COALESCE(p.owner_crew_id,''),COALESCE(p.folder_id,''),
 EXISTS(SELECT 1 FROM documents d WHERE d.workspace_id=p.workspace_id AND d.page_id=p.id),
 EXISTS(SELECT 1 FROM slides_heads s WHERE s.workspace_id=p.workspace_id AND s.page_id=p.id)
 FROM pages p WHERE p.workspace_id=? AND
 (EXISTS(SELECT 1 FROM documents d WHERE d.workspace_id=p.workspace_id AND d.page_id=p.id) OR
  EXISTS(SELECT 1 FROM slides_heads s WHERE s.workspace_id=p.workspace_id AND s.page_id=p.id))
 ORDER BY p.id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]nativeSearchCandidate, 0)
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var page pageRecord
		var candidate nativeSearchCandidate
		if err := rows.Scan(&page.ID, &page.Slug, &page.OwnerUserID, &page.OwnerCrewID, &page.FolderID, &candidate.Document, &candidate.Slides); err != nil {
			return nil, err
		}
		panels, err := h.assetPagePanels(ctx, page.ID)
		if err != nil {
			return nil, err
		}
		visible, err := h.canSeePage(ctx, workspace, &page, panels, viewer)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}
		if len(candidates) == maxNativeSearchCandidates {
			return nil, errNativeSearchCandidates
		}
		candidate.PageID, candidate.Slug = page.ID, page.Slug
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// The same per-call live credential/role reader is used before candidate scan
// and inside projection read/publication transactions after managed IPC.
func (h *PageHandler) nativeSearchViewer(ctx context.Context, r *http.Request, tx *sql.Tx) (context.Context, *pageViewer, error) {
	if tx == nil {
		return ctx, nil, errNativeSearchAuthentication
	}
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		return ctx, nil, errNativeSearchAuthentication
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if err := validateContentHumanCredential(r.WithContext(ctx), tx, "pages:read"); err != nil {
		return ctx, nil, fmt.Errorf("%w: %w", errNativeSearchAuthentication, err)
	}
	role, err := h.liveContentHumanRole(ctx, WorkspaceIDFromContext(ctx), user.ID)
	if err != nil {
		return ctx, nil, errNativeSearchMembership
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	viewer, err := h.loadViewer(ctx, WorkspaceIDFromContext(ctx), user.ID)
	return ctx, viewer, err
}
