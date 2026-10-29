package api

import "context"

// Resolve the installed family instead of allowing one feature to enable another.
func (h *PageHandler) artifactRunFamilyEnabled(ctx context.Context, workspace, page string) bool {
	if current := WorkspaceIDFromContext(ctx); current != "" && current != workspace {
		return false
	}
	ctx = context.WithValue(ctx, ctxWorkspaceID, workspace)
	if page == "" {
		return h.artifactProjectEnabled(ctx)
	}
	var family string
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT content_type FROM artifact_version_heads WHERE page_id=? AND workspace_id=?`, page, workspace).Scan(&family); err != nil {
		return false
	}
	if family == "design" {
		return h.designEnabled(ctx)
	}
	return family == "artifact_project" && h.artifactProjectEnabled(ctx)
}

// Versioned canvases cannot be replaced through the panels or project editors.
func (h *PageHandler) artifactContentAttached(ctx context.Context, page string) (bool, error) {
	var attached bool
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_version_heads WHERE page_id=? AND content_type IN ('artifact_project','design'))`, page).Scan(&attached)
	return attached, err
}
