package api

import (
	"context"
	"database/sql"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/crewship-ai/crewship/internal/ws"
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
	return trustedRecheckUsers(ctx, tx, set)
}

// trustedRecheckUsers keeps only people the realtime layer may deliver to:
// not suspended and not under the restricted-profile ceiling (the same rule as
// ws.trustedChannelMember). Others get no frame; their reads are refused anyway.
func trustedRecheckUsers(ctx context.Context, tx *sql.Tx, set map[string]bool) ([]string, error) {
	all := make([]string, 0, len(set))
	for id := range set {
		if id != "" {
			all = append(all, id)
		}
	}
	sort.Strings(all)
	users := make([]string, 0, len(all))
	for start := 0; start < len(all); start += recheckBatch {
		chunk := all[start:min(start+recheckBatch, len(all))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, `SELECT u.id FROM users u WHERE u.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)
 AND u.suspended_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members r WHERE r.user_id=u.id AND r.access_mode='restricted')`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			users = append(users, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(users)
	return users, nil
}

const recheckBatch = 500

// recheckSlots bounds how many fan-outs run at once; a fan-out waits for a slot
// inside its own goroutine, never on the request goroutine.
var recheckSlots = make(chan struct{}, 4)

// recheckInFlight lets tests wait for the asynchronous sends.
var recheckInFlight sync.WaitGroup

// publishUserAccessRecheck sends payload to each user's own channel after the
// mutation committed. The request returns at once; the sends run in a bounded
// goroutine, in batches that yield to other hub traffic. A failure (including
// a panic) is logged and never affects the response: the 30 s poll is the fallback.
func publishUserAccessRecheck(hub *ws.Hub, logger *slog.Logger, users []string, payload map[string]any) {
	if hub == nil || len(users) == 0 {
		return
	}
	recheckInFlight.Add(1)
	go func() {
		defer recheckInFlight.Done()
		recheckSlots <- struct{}{}
		defer func() { <-recheckSlots }()
		sent := 0
		defer func() {
			if r := recover(); r != nil && logger != nil {
				logger.Error("page access recheck fan-out failed", "recipients", len(users), "sent", sent, "panic", r)
			}
		}()
		for start := 0; start < len(users); start += recheckBatch {
			for _, user := range users[start:min(start+recheckBatch, len(users))] {
				hub.BroadcastChannel("user", user, pageAccessRecheckEvent, payload)
				sent++
			}
			runtime.Gosched()
		}
	}()
}

// publishNativeCraftAccessRecheck runs after the grant commit.
func (h *PageHandler) publishNativeCraftAccessRecheck(pageID string, users []string) {
	if h == nil {
		return
	}
	publishUserAccessRecheck(h.hub, h.logger, users, map[string]any{"page_id": pageID})
}
