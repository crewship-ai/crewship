package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// issueParentError is a refusal the caller maps straight to an HTTP status.
type issueParentError struct {
	status int
	msg    string
}

func (e *issueParentError) Error() string { return e.msg }

// authorizeIssueParent decides whether an agent creating an issue in
// childCrewID may make it a sub-issue of parentIdentifier, inside the
// creating transaction so the decision and the write cannot drift apart.
//
// This is the authorized decomposition path. It does NOT widen what a run
// may touch:
//
//   - The parent row is never written. Only the new child's parent_issue_id
//     is set, so the invariant "an agent only mutates issues in its own crew"
//     holds exactly as for create. The parent is still affected: its
//     sub-issue list grows, and an open child holds back closing it (the
//     terminal-children rule). A connection from the child's crew is what
//     permits that effect on a connected crew's issue.
//   - The parent must be an issue of the child's own crew, or of a crew the
//     child's crew is connected to (same rule as AreCrewsConnected: an active
//     connection from the child's crew, or a bidirectional one either way). Unrelated crews' work stays out of
//     reach — stricter than the standalone sub_issue_of link, which only
//     checks the workspace.
//   - Under human authority (a chat question, or an issue a person started),
//     that person must currently be allowed to update issues: attaching work
//     under an issue is a structural edit of it, the same verb the public
//     route requires.
//
// A closed (terminal) or in-REVIEW parent is a 409, checked after the
// requester's right to update so the status is not revealed first.
//
// A foreign-workspace, unknown or non-issue identifier is a plain 404, not a
// distinguishable refusal (no existence oracle).
func authorizeIssueParent(ctx context.Context, tx *sql.Tx, op *chatIssueOperation, workspaceID, childCrewID, parentIdentifier string) (string, error) {
	var parentID, parentCrew, parentStatus string
	err := tx.QueryRowContext(ctx,
		`SELECT id, crew_id, status FROM missions
		 WHERE identifier = ? AND workspace_id = ? AND mission_type = 'issue'`,
		parentIdentifier, workspaceID).Scan(&parentID, &parentCrew, &parentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &issueParentError{http.StatusNotFound, "Parent issue not found: " + parentIdentifier}
	}
	if err != nil {
		return "", err
	}
	if parentCrew != childCrewID {
		var connected bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM crew_connections
			WHERE status = 'active' AND (
				(from_crew_id = ? AND to_crew_id = ?)
				OR (from_crew_id = ? AND to_crew_id = ? AND direction = 'bidirectional')))`,
			childCrewID, parentCrew, parentCrew, childCrewID).Scan(&connected); err != nil {
			return "", err
		}
		if !connected {
			return "", &issueParentError{http.StatusForbidden,
				"parent issue " + parentIdentifier + " belongs to a crew this crew is not connected to"}
		}
	}
	if op != nil && op.authority.HumanAuthority != nil {
		allowed, err := issueRequesterAllowed(ctx, tx, op.authority.HumanAuthority.UserID, workspaceID, "updated")
		if err != nil {
			return "", err
		}
		if !allowed {
			return "", &issueParentError{http.StatusForbidden,
				"the requesting person may not update issues, so work cannot be attached under " + parentIdentifier}
		}
	}
	// A parent in DONE/REVIEW (or otherwise closed) with a new open child is
	// exactly the state the terminal-children rule refuses on a status
	// change (isTerminalIssueTarget); splitting work under it is a mistake the
	// caller should see, not a silent reopening hazard.
	if terminalIssueStatuses[parentStatus] || isTerminalIssueTarget(parentStatus) {
		return "", &issueParentError{http.StatusConflict,
			"parent issue " + parentIdentifier + " is " + parentStatus + "; reopen it before attaching new work"}
	}
	return parentID, nil
}
