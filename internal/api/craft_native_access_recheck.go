package api

import (
	"context"
	"database/sql"
	"sort"
)

// pageAccessRecheckEvent is the push that tells ONE person their access to ONE
// native Craft page may have changed. It carries the page id and nothing else:
// no content, no level, no names of other people. The receiver re-reads through
// its own authorised HTTP path and drops what it shows on 403/404, so the push
// can neither grant nor reveal anything. It travels on the identity channel
// user:{id}, which only that person may subscribe to.
const pageAccessRecheckEvent = "page.access_recheck"

// nativeCraftRecheckUsers names the people a grant mutation can affect: the
// named user, the members of a named crew, or, for a workspace-wide grant, the
// workspace members. Agents are not human viewers. Resolved inside the grant
// transaction, before it commits.
func nativeCraftRecheckUsers(ctx context.Context, tx *sql.Tx, workspace string, changes []pageGrantChange) ([]string, error) {
	set := map[string]bool{}
	for _, c := range changes {
		g := c.previous
		if g == nil {
			g = c.next
		}
		if g == nil {
			continue
		}
		var query string
		var args []any
		switch g.SubjectType {
		case pageSubjectUser:
			set[g.SubjectID] = true
			continue
		case pageSubjectCrew:
			query, args = `SELECT user_id FROM crew_members WHERE crew_id=? AND user_id IS NOT NULL AND user_id<>''`, []any{g.SubjectID}
		case pageSubjectWorkspace:
			query, args = `SELECT user_id FROM workspace_members WHERE workspace_id=?`, []any{workspace}
		default:
			continue
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			set[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	users := make([]string, 0, len(set))
	for id := range set {
		users = append(users, id)
	}
	sort.Strings(users)
	return users, nil
}

// publishNativeCraftAccessRecheck runs after the grant commit.
func (h *PageHandler) publishNativeCraftAccessRecheck(pageID string, users []string) {
	if h == nil || h.hub == nil {
		return
	}
	for _, user := range users {
		h.hub.BroadcastChannel("user", user, pageAccessRecheckEvent, map[string]any{"page_id": pageID})
	}
}
