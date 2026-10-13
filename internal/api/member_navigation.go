package api

import (
	"context"
	"errors"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// The static allowlist is the account ceiling. This projection narrows it to
// the selected membership's authorized content, independent of runtime health.
func (h *WorkspaceHandler) memberRestrictedSurfaces(ctx context.Context, user, workspace string) ([]string, error) {
	var chat bool
	err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN workspace_members wm ON wm.workspace_id=a.workspace_id AND wm.user_id=? JOIN access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation IN ('discover','chat') WHERE a.workspace_id=? AND a.deleted_at IS NULL)`, user, workspace).Scan(&chat)
	if err != nil {
		return nil, err
	}
	routines, err := restrictedworkflow.AuthorizedCatalog(ctx, h.db, user, workspace)
	if err != nil && !errors.Is(err, restrictedworkflow.ErrDenied) {
		return nil, err
	}
	pageHandler := NewPageHandler(h.db, nil, h.logger)
	pages, err := pageHandler.restrictedPageCatalog(ctx, user, workspace, func(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation) (string, error) {
		return restrictedworkflow.AuthorizedPageActionFingerprint(ctx, h.db, user, workspace, action)
	})
	if err != nil {
		return nil, err
	}
	available := map[string]bool{"chat": chat, "routines": len(routines) > 0, "pages": len(pages) > 0, "account_security": true}
	result := []string{}
	for _, surface := range allowedRestrictedSurfaces() {
		if available[surface] {
			result = append(result, surface)
		}
	}
	return result, nil
}
