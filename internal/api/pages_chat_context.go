package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

// ResolvePageChatContext is the use-time authority for a Page reference in a
// chat message. The client supplies only a slug; this method derives every
// field from the current Page and its current, issuer-validated agent grants.
// A missing Page, human membership, human reach, agent, or agent grant all
// return the same refusal so the chat path cannot enumerate hidden Pages.
func (h *PageHandler) ResolvePageChatContext(ctx context.Context, workspaceID, userID, agentID, slug string) (chatbridge.PageChatContext, error) {
	deniedContext, deniedError := chatbridge.PageChatContext{}, chatbridge.ErrPageChatAccess
	if workspaceID == "" || userID == "" || agentID == "" || slug == "" || len(slug) > 128 {
		return deniedContext, deniedError
	}
	// The resolver also serves internal callers that pass the validated human
	// identity explicitly. Never replace a different authenticated principal.
	if user := UserFromContext(ctx); user != nil && user.ID != userID {
		return deniedContext, deniedError
	} else if user == nil {
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	}
	var role string
	if err := h.db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspaceID, userID).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deniedContext, deniedError
		}
		return chatbridge.PageChatContext{}, err
	}
	var liveAgent string
	if err := h.db.QueryRowContext(ctx, `SELECT id FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND expired_at IS NULL`, agentID, workspaceID).Scan(&liveAgent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deniedContext, deniedError
		}
		return chatbridge.PageChatContext{}, err
	}
	rec, err := h.loadPageForRead(ctx, workspaceID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deniedContext, deniedError
		}
		return chatbridge.PageChatContext{}, err
	}
	viewer, err := h.loadViewer(ctx, workspaceID, userID)
	if err != nil {
		return chatbridge.PageChatContext{}, err
	}
	viewer.Role = role
	if rec.PrivateAudience {
		current, err := h.livePrivatePageReader(ctx, workspaceID, userID)
		if err != nil {
			return chatbridge.PageChatContext{}, err
		}
		if current == nil {
			return deniedContext, deniedError
		}
		viewer.Role = current.Role
	}
	panels, err := h.loadPanels(ctx, workspaceID, rec.ID)
	if err != nil {
		return chatbridge.PageChatContext{}, err
	}
	visible, err := h.canSeePage(ctx, workspaceID, rec, panels, viewer)
	if err != nil {
		return chatbridge.PageChatContext{}, err
	}
	if !visible {
		return deniedContext, deniedError
	}
	grants, err := h.loadPageGrantRecords(ctx, workspaceID, rec)
	if err != nil {
		return chatbridge.PageChatContext{}, err
	}
	if len(pageAccessAgentPaths(agentID, grants)) == 0 {
		return deniedContext, deniedError
	}
	if err := h.recordPrivateOwnerAccess(ctx, workspaceID, userID, rec); err != nil {
		return deniedContext, err
	}
	return chatbridge.PageChatContext{
		WorkspaceID: workspaceID, PageID: rec.ID, Slug: rec.Slug,
		Name: rec.Name, SnapshotAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}
