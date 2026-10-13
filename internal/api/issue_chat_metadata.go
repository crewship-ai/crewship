package api

import "net/http"

// loadChatMetadata keeps card capabilities and field/subresource freshness on
// the existing issue response. Mutations still perform their own live checks.
func (h *IssueHandler) loadChatMetadata(r *http.Request, issue *issueResponse) error {
	issue.AllowedActions = []string{}
	user := UserFromContext(r.Context())
	if user == nil {
		return nil
	}
	actions, err := issueViewerActions(r.Context(), h.db, user.ID, issue.WorkspaceID, issue.CrewID)
	if err != nil {
		return err
	}
	if !canScope(r.Context(), scopeForRoute("/api/v1/crews/{crewId}/issues/{identifier}")) {
		actions = []string{"view"}
	}
	issue.AllowedActions = actions
	return nil
}
