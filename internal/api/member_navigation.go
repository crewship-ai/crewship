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
	err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents a JOIN workspace_members wm ON wm.workspace_id=a.workspace_id AND wm.user_id=? JOIN live_access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation='discover' WHERE a.workspace_id=? AND a.deleted_at IS NULL)`, user, workspace).Scan(&chat)
	if err != nil {
		return nil, err
	}
	eligible, err := restrictedworkflow.HasAnyWorkflowRunAuthority(ctx, h.db, user, workspace)
	if err != nil {
		return nil, err
	}
	routines := false
	var pages []restrictedPage
	if eligible {
		var profiles func(string) bool
		if h.restrictedWorkflow != nil {
			profiles = h.restrictedWorkflow().DeclaredProfileSupport()
		}
		routines, err = restrictedworkflow.HasAuthorizedRoutine(ctx, h.db, user, workspace, profiles)
		if err != nil && !errors.Is(err, restrictedworkflow.ErrDenied) {
			return nil, err
		}
		pageHandler := NewPageHandler(h.db, nil, h.logger)
		pages, err = pageHandler.restrictedPageCatalog(ctx, user, workspace, func(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation) (string, error) {
			return restrictedworkflow.AuthorizedPageActionFingerprint(ctx, h.db, user, workspace, action, profiles)
		}, true)
		if err != nil {
			return nil, err
		}
	}
	var issues bool
	if err = h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM live_access_grants g JOIN workspace_members wm ON wm.id=g.member_id JOIN projects p ON p.id=g.project_id AND p.workspace_id=wm.workspace_id WHERE wm.user_id=? AND wm.workspace_id=? AND g.resource_kind='project' AND g.operation='list')`, user, workspace).Scan(&issues); err != nil {
		return nil, err
	}
	available := map[string]bool{"issues": issues, "chat": chat, "routines": routines, "pages": len(pages) > 0, "account_security": true}
	result := []string{}
	for _, surface := range allowedRestrictedSurfaces() {
		if available[surface] {
			result = append(result, surface)
		}
	}
	return result, nil
}
