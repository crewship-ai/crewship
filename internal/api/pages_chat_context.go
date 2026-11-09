package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts/kindguide"
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
	out := chatbridge.PageChatContext{
		WorkspaceID: workspaceID, PageID: rec.ID, Slug: rec.Slug,
		Name: rec.Name, SnapshotAt: time.Now().UTC().Format(time.RFC3339),
	}
	// The kind is derived from the same server metadata the library shows,
	// for the version this human may read; it only names a static guide.
	storage, _, _ := h.artifactPageMetadata(ctx, workspaceID, rec.ID)
	designSystem := storage == "project" && h.artifactPageHasDesignSystem(ctx, workspaceID, rec.ID)
	if kind := pageChatKind(rec.HasDocument, rec.HasSlides, storage, designSystem); kind != "" {
		if guide, err := kindguide.Lookup(kind); err == nil {
			out.Kind, out.GuideVersion = guide.Kind, guide.Version
		}
	}
	return out, nil
}

// pageChatKind maps server storage to an Artifact kind guide. It is a
// fail-closed subset of the library's kindOf
// (components/features/artifacts-library/kinds.ts): Craft (word, sheet),
// panel Pages, boards, Mooring and published applications get no guide rather
// than a guessed one, and a Dashboard or Slides project keeps that kind even
// when it also carries design-system.json.
func pageChatKind(hasDocument, hasSlides bool, artifactKind string, designSystem bool) string {
	switch {
	case artifactKind == "word" || artifactKind == "sheet":
		return "" // native Craft: the crewship-craft playbook applies
	case hasDocument:
		return "docs"
	case hasSlides:
		return "slides"
	}
	switch artifactKind {
	case "project":
		if designSystem {
			return "library"
		}
		return "app"
	case "dashboard", "slides", "design":
		return artifactKind
	}
	return ""
}

// artifactPageHasDesignSystem reports whether the project version this caller
// reads (latest for the owner, shared otherwise) carries design-system.json.
func (h *PageHandler) artifactPageHasDesignSystem(ctx context.Context, workspace, page string) bool {
	user := UserFromContext(ctx)
	if user == nil {
		return false
	}
	var present bool
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT json_extract(v.manifest_json,'$.files."design-system.json".hash') IS NOT NULL
 FROM pages p JOIN artifact_version_heads h ON h.workspace_id=p.workspace_id AND h.page_id=p.id
 JOIN artifact_versions v ON v.workspace_id=h.workspace_id AND v.page_id=h.page_id AND v.version=CASE WHEN p.owner_user_id=? THEN h.latest_version ELSE h.shared_version END
 WHERE p.workspace_id=? AND p.id=? AND h.content_type='artifact_project'`, user.ID, workspace, page).Scan(&present)
	if err != nil && err != sql.ErrNoRows {
		h.logger.Error("artifact design system marker unavailable", "error", err)
	}
	return err == nil && present
}
