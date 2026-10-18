package api

import (
	"net/http"
	"strings"
)

// Quick views for GET /api/v1/issues (Harbor 2, ODCH-ISS-03).
//
// The Issues panel offers six views. A client cannot build them from the page
// it loaded (at most 100 rows) without leaving issues out, so each is a
// server-side filter that joins the same WHERE clause as the page, the total
// and the status counts. Every view is workspace-scoped like the plain list:
// the sessions and relations it looks at are reached only from a mission of
// the caller's workspace.
//
//	owner_id=<user id|me>    the human owner (me = the caller)
//	delegate_id=<agent id>   the agent delegate
//	work_mode=agent|human    who holds the work (issue_work.mode)
//	mine=1                   open issues I own or that I hold as the worker
//	working=1                an agent holds it and its session is active
//	needs=reply              open, I own it, an agent session is awaiting_input
//	needs=review             in REVIEW and I own it
//	blocked=1                an open issue blocks it (mission_relations)
//
// "Open" is any status outside DONE, COMPLETED, CANCELLED and DUPLICATE.
// Personal views (me, mine, needs) match nothing when the request carries no
// user, rather than falling back to every issue.

const issueClosedStatuses = `('DONE','COMPLETED','CANCELLED','DUPLICATE')`

// issueViewParams are the raw quick-view query values. List reads them
// itself (so the OpenAPI generator, which scans the handler body, sees the
// parameters) and hands them over.
type issueViewParams struct {
	OwnerID, DelegateID, WorkMode, Mine, Working, Needs, Blocked string
}

// applyIssueViewFilters appends the quick-view conditions to where/args. The
// last result is a message, empty when every value is in the vocabulary.
func applyIssueViewFilters(r *http.Request, p issueViewParams, where string, args []interface{}) (string, []interface{}, string) {
	me := ""
	if u := UserFromContext(r.Context()); u != nil {
		me = u.ID
	}
	// Personal conditions compare against the caller; with no caller the
	// condition is false ("1 = 0"), not skipped.
	personal := func(cond string) {
		if me == "" {
			where += " AND 1 = 0"
			return
		}
		where += " AND " + cond
		args = append(args, me)
	}

	if v := strings.TrimSpace(p.OwnerID); v != "" {
		if v == "me" {
			personal("m.owner_user_id = ?")
		} else {
			where += " AND m.owner_user_id = ?"
			args = append(args, v)
		}
	}
	if v := strings.TrimSpace(p.DelegateID); v != "" {
		where += " AND m.delegate_agent_id = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(p.WorkMode); v != "" {
		if v != "agent" && v != "human" {
			return where, args, "work_mode must be agent or human"
		}
		where += " AND COALESCE(iw.mode, 'agent') = ?"
		args = append(args, v)
	}
	if p.Mine == "1" {
		if me == "" {
			where += " AND 1 = 0"
		} else {
			where += " AND m.status NOT IN " + issueClosedStatuses +
				" AND (m.owner_user_id = ? OR (iw.mode = 'human' AND iw.worker_user_id = ?))"
			args = append(args, me, me)
		}
	}
	if p.Working == "1" {
		where += " AND m.status NOT IN " + issueClosedStatuses +
			" AND COALESCE(iw.mode, 'agent') = 'agent'" +
			" AND EXISTS (SELECT 1 FROM issue_agent_sessions s WHERE s.mission_id = m.id AND s.state = 'active')"
	}
	switch v := strings.TrimSpace(p.Needs); v {
	case "":
	case "reply":
		personal("m.status NOT IN " + issueClosedStatuses + " AND m.owner_user_id = ?" +
			" AND EXISTS (SELECT 1 FROM issue_agent_sessions s WHERE s.mission_id = m.id AND s.state = 'awaiting_input')")
	case "review":
		personal("m.status = 'REVIEW' AND m.owner_user_id = ?")
	default:
		return where, args, "needs must be reply or review"
	}
	if p.Blocked == "1" {
		where += " AND m.status NOT IN " + issueClosedStatuses +
			" AND EXISTS (SELECT 1 FROM mission_relations rel JOIN missions b ON b.id = rel.source_id" +
			" WHERE rel.target_id = m.id AND rel.relation_type = 'blocks' AND b.status NOT IN " + issueClosedStatuses + ")"
	}
	return where, args, ""
}
