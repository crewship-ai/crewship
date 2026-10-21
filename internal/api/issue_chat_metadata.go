package api

import "net/http"

// Capability output describes both current permission and execution state.
// Every mutation still rechecks these gates at its own enforcement point.
func (h *IssueHandler) loadChatMetadata(r *http.Request, issue *issueResponse) error {
	return h.loadChatMetadataQuery(r, h.db, issue)
}
func (h *IssueHandler) loadChatMetadataQuery(r *http.Request, q issueOperationQueryer, issue *issueResponse) error {
	issue.AllowedActions = []string{}
	user := UserFromContext(r.Context())
	if user == nil {
		return nil
	}
	actions, err := issueViewerActions(r.Context(), q, user.ID, issue.WorkspaceID)
	if err != nil {
		return err
	}
	if len(actions) > 0 && !canScope(r.Context(), scopeForRoute("/api/v1/crews/{crewId}/issues/{identifier}")) {
		actions = []string{"view"}
	}
	var busy bool
	if err = q.QueryRowContext(r.Context(), `SELECT `+issueCardBusySQL+` FROM missions m WHERE m.id=? AND m.workspace_id=?`, issue.ID, issue.WorkspaceID).Scan(&busy); err != nil {
		return err
	}
	issue.AllowedActions = issueActionsForState(actions, issue.Status, busy)
	return nil
}

const issueCardBusySQL = `(EXISTS(SELECT 1 FROM issue_work iw WHERE iw.mission_id=m.id AND iw.mode='human')
 OR EXISTS(SELECT 1 FROM assignments x WHERE x.mission_id=m.id AND x.status IN ('PENDING','QUEUED','RUNNING'))
 OR EXISTS(SELECT 1 FROM issue_executions x JOIN pipeline_runs pr ON pr.id=x.routine_run_id WHERE x.mission_id=m.id AND pr.status IN ('queued','running','waiting')))`

func issueActionsForState(actions []string, status string, busy bool) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		switch action {
		case "delete":
			if status != "BACKLOG" && status != "CANCELLED" {
				continue
			}
		case "start_work":
			if busy || (status != "BACKLOG" && status != "TODO") {
				continue
			}
		case "change_workflow":
			if status == "DONE" || status == "COMPLETED" || status == "CANCELLED" || status == "DUPLICATE" {
				continue
			}
		}
		out = append(out, action)
	}
	return out
}
