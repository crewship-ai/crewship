package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

type guestIssueDeletion struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	ProjectID  string `json:"project_id"`
	Status     string `json:"status"`
	Version    string `json:"version"`
	CanDelete  bool   `json:"can_delete"`
}

// Dedicated preview avoids exposing deletion snapshots through Read-only lists.
func (r *Router) restrictedIssueDeletion(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Issue unavailable")
		return
	}
	var body struct {
		ProjectID string `json:"project_id"`
		Version   string `json:"version"`
		Confirm   bool   `json:"confirm"`
	}
	write := req.Method == "DELETE"
	if write {
		if !readGuestIssueJSON(w, req, &body) {
			return
		}
		if body.ProjectID == "" || len(body.Version) != 64 || !body.Confirm {
			replyError(w, 400, "Review the issue project and version, and explicitly confirm deletion")
			return
		}
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	defer tx.Rollback()
	issue, err := r.restrictedIssueIn(req.Context(), tx, user.ID, workspace, req.PathValue("issueId"), false)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if err = (access.Store{DB: r.db}).CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "project", ID: issue.ProjectID, Operation: "manage"}); err != nil {
		guestIssueError(w, err)
		return
	}
	if err = (access.Store{DB: r.db}).CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "project", ID: issue.ProjectID, Operation: "delete_issues"}); err != nil {
		guestIssueError(w, err)
		return
	}
	var revision, aggregate int64
	var crew, updated string
	if err = tx.QueryRowContext(req.Context(), `SELECT revision,aggregate_revision,crew_id,updated_at FROM missions WHERE id=? AND workspace_id=? AND project_id=?`, issue.ID, workspace, issue.ProjectID).Scan(&revision, &aggregate, &crew, &updated); err != nil {
		guestIssueError(w, err)
		return
	}
	active, err := guestIssueActiveWorkIn(req.Context(), tx, issue.ID)
	if err != nil {
		guestIssueError(w, err)
		return
	}
	var children bool
	if err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM missions WHERE parent_issue_id=?)`, issue.ID).Scan(&children); err != nil {
		guestIssueError(w, err)
		return
	}
	raw, _ := json.Marshal([]any{issue.ID, issue.ProjectID, issue.Status, revision, aggregate, updated})
	sum := sha256.Sum256(raw)
	current := guestIssueDeletion{ID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, ProjectID: issue.ProjectID, Status: issue.Status, Version: hex.EncodeToString(sum[:]), CanDelete: canScope(req.Context(), "workspace:admin") && !active && !children && (issue.Status == "BACKLOG" || issue.Status == "CANCELLED")}
	if !write {
		writeJSON(w, 200, current)
		return
	}
	if !canScope(req.Context(), "workspace:admin") {
		replyError(w, 403, "Credential does not allow this change")
		return
	}
	if body.ProjectID != issue.ProjectID || body.Version != current.Version {
		replyError(w, 409, "Issue changed. Review its current project and version before deleting.")
		return
	}
	if !current.CanDelete {
		replyError(w, 409, "Only Backlog or Cancelled issues without active work or sub-issues can be deleted")
		return
	}
	// Collect exact digests before the cascade; filesystem cleanup is only after
	// a successful commit and never scans other projects or workspaces.
	var digests []string
	if r.storagePath != "" {
		rows, e := tx.QueryContext(req.Context(), `SELECT DISTINCT a.sha256 FROM attachments a WHERE a.workspace_id=? AND (a.mission_id=? OR a.comment_id IN (SELECT id FROM mission_comments WHERE mission_id=?))`, workspace, issue.ID, issue.ID)
		if e != nil {
			guestIssueError(w, e)
			return
		}
		for rows.Next() {
			var digest string
			if e = rows.Scan(&digest); e != nil {
				break
			}
			digests = append(digests, digest)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			guestIssueError(w, e)
			return
		}
		if rowErr != nil {
			guestIssueError(w, rowErr)
			return
		}
	}
	if _, err = tx.ExecContext(req.Context(), `PRAGMA defer_foreign_keys = ON`); err != nil {
		guestIssueError(w, err)
		return
	}
	if _, err = tx.ExecContext(req.Context(), `UPDATE assignments SET issue_execution_id=NULL WHERE issue_execution_id IN (SELECT id FROM issue_executions WHERE mission_id=?)`, issue.ID); err != nil {
		guestIssueError(w, err)
		return
	}
	if _, err = tx.ExecContext(req.Context(), `DELETE FROM missions WHERE id=? AND workspace_id=? AND project_id=?`, issue.ID, workspace, issue.ProjectID); err != nil {
		guestIssueError(w, err)
		return
	}
	if err = appendHumanIssueDeletionReceipts(req.Context(), tx, workspace, issue.ID, user.ID, revision, aggregate); err != nil {
		guestIssueError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryAuditEntityDeleted, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest deleted a project issue", Payload: map[string]any{"entity_type": "issue", "issue_id": issue.ID, "project_id": issue.ProjectID}})
	if err != nil {
		guestIssueError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		guestIssueError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	if r.storagePath != "" {
		reclaimAttachmentDigests(req.Context(), r.db, r.logger, r.storagePath, workspace, digests)
	}
	broadcastIssueCardInvalidations(req.Context(), r.db, r.hub, workspace, issue.ID, true)
	broadcastWorkspaceEvent(r.hub, workspace, "issue.deleted", map[string]string{"identifier": issue.Identifier, "crew_id": crew})
	w.WriteHeader(204)
}
