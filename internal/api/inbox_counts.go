package api

import (
	"net/http"
	"strings"
	"time"
)

// inboxSpace names the three Inbox spaces the Harbor 2 UI sorts items into.
// The wire keys match the UI's view ids so a client does not translate.
const (
	inboxSpaceAction  = "action"  // active and a person can act on it
	inboxSpaceUpdates = "updates" // active and informational
	inboxSpaceHistory = "history" // resolved
)

// inboxTextNonEmpty is true when payload key is a non-empty JSON string. The
// client's payloadString() ignores non-string values, so
// a bare json_extract(...) comparison would count a number or an object as present.
func inboxTextNonEmpty(key string) string {
	return `(json_type(payload_json, '$.` + key + `') = 'text' AND json_extract(payload_json, '$.` + key + `') != '')`
}

// inboxActionableSQL is isActionableInboxItem (components/features/inbox-v2/
// inbox-v2-derive.ts) as a predicate over one active inbox_items row: is there
// a real source action behind it. The two must stay in step; the counts test
// pins every branch. Callers AND it with state != 'resolved'.
var inboxActionableSQL = `(
	kind = 'waitpoint'
	OR (kind = 'escalation' AND (
		(json_type(payload_json, '$.kind') = 'text' AND json_extract(payload_json, '$.kind') IN ('skill_proposal', 'routine_proposal'))
		OR ` + inboxTextNonEmpty("escalation_type") + `
		OR (json_extract(payload_json, '$.request_type') = 'access' AND ` + inboxTextNonEmpty("request_id") + `)
	))
	OR (kind != 'escalation' AND blocking = 1)
	OR (kind = 'memory_consolidation' AND ` + inboxTextNonEmpty("proposal_id") + `)
	OR (kind IN ('schedule_missed', 'schedule_circuit_breaker_tripped') AND ` + inboxTextNonEmpty("schedule_id") + `)
)`

// inboxSpaceCounts is one bucket set: items per space.
type inboxSpaceCounts struct {
	Action  int `json:"action"`
	Updates int `json:"updates"`
	History int `json:"history"`
}

func (c *inboxSpaceCounts) add(space string, n int) {
	switch space {
	case inboxSpaceAction:
		c.Action += n
	case inboxSpaceUpdates:
		c.Updates += n
	case inboxSpaceHistory:
		c.History += n
	}
}

// inboxCountsResponse is GET /api/v1/inbox/counts. Counts are inbox items, not
// the client's merged entries: approvals-queue rows and issue tasks that the
// page adds, and the grouping of repeated advisories, stay client-side.
type inboxCountsResponse struct {
	Total  int                         `json:"total"`
	Unread int                         `json:"unread"`
	Spaces inboxSpaceCounts            `json:"spaces"`
	ByKind map[string]inboxSpaceCounts `json:"by_kind"`
	// ByAttention and ByDeadline are the explorer's facet counts, over the same
	// rows: active items per attention class, and items per deadline bucket
	// (hour / today / soon / none; soon overlaps the other buckets).
	ByAttention map[string]int `json:"by_attention"`
	ByDeadline  map[string]int `json:"by_deadline"`
}

var (
	inboxAttentionKeys = []string{inboxAttentionDecision, inboxAttentionInput, inboxAttentionReview, inboxAttentionRepair}
	inboxDeadlineKeys  = []string{"hour", "today", "soon", "none"}
)

// Counts serves GET /api/v1/inbox/counts: totals per space and per kind for
// the caller's visible items, so a client need not walk every page of the list
// just to label its tabs. Visibility, per-caller unread and the active /
// resolved split are the list endpoint's own expressions.
func (h *InboxHandler) Counts(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	// The same optional filters as the list (q, space, unread, attention,
	// deadline, crew, agent, kind), so a count and the rows it labels agree.
	filters, badFilter := parseInboxFilters(r.URL.Query(), time.Now)
	if badFilter != "" {
		replyError(w, http.StatusBadRequest, badFilter)
		return
	}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		filters.clause += " AND kind = ?"
		filters.args = append(filters.args, kind)
	}
	query := `SELECT kind,
		CASE WHEN state = 'resolved' THEN '` + inboxSpaceHistory + `'
		     WHEN ` + inboxActionableSQL + ` THEN '` + inboxSpaceAction + `'
		     ELSE '` + inboxSpaceUpdates + `' END AS space,
		COUNT(*),
		SUM(CASE WHEN ` + inboxEffectiveStateExpr + ` = 'unread' THEN 1 ELSE 0 END)
	FROM inbox_items` + inboxReadsJoinClause + ` WHERE workspace_id = ?` + visClause + filters.clause + ` GROUP BY kind, space`
	args := append([]interface{}{user.ID, workspaceID}, visArgs...)
	args = append(args, filters.args...)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("inbox counts", "error", err)
		replyError(w, http.StatusInternalServerError, "counts failed")
		return
	}
	defer rows.Close()

	out := inboxCountsResponse{
		ByKind:      map[string]inboxSpaceCounts{},
		ByAttention: map[string]int{},
		ByDeadline:  map[string]int{},
	}
	for rows.Next() {
		var kind, space string
		var n, unread int
		if err := rows.Scan(&kind, &space, &n, &unread); err != nil {
			// A partial aggregate must not read as a smaller inbox.
			h.logger.Error("inbox counts scan", "error", err)
			replyError(w, http.StatusInternalServerError, "counts failed")
			return
		}
		out.Total += n
		out.Unread += unread
		out.Spaces.add(space, n)
		k := out.ByKind[kind]
		k.add(space, n)
		out.ByKind[kind] = k
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("inbox counts iterate", "error", err)
		replyError(w, http.StatusInternalServerError, "counts failed")
		return
	}
	if err := h.facetCounts(r, &out, user.ID, workspaceID, visClause, visArgs, filters); err != nil {
		h.logger.Error("inbox facet counts", "error", err)
		replyError(w, http.StatusInternalServerError, "counts failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// facetCounts fills ByAttention and ByDeadline in one pass over the filtered
// rows. The deadline buckets carry bound instants, and those placeholders come
// first in the statement, ahead of the reads join.
func (h *InboxHandler) facetCounts(r *http.Request, out *inboxCountsResponse, userID, workspaceID, visClause string, visArgs []interface{}, f inboxFilters) error {
	var cols []string
	var args []interface{}
	for _, k := range inboxAttentionKeys {
		cols = append(cols, `COALESCE(SUM(CASE WHEN `+inboxAttentionSQL+` = '`+k+`' THEN 1 ELSE 0 END), 0)`)
	}
	for _, k := range inboxDeadlineKeys {
		sql, a := f.clock.deadlineSQL(k)
		cols = append(cols, `COALESCE(SUM(CASE WHEN `+sql+` THEN 1 ELSE 0 END), 0)`)
		args = append(args, a...)
	}
	query := `SELECT ` + strings.Join(cols, ", ") + ` FROM inbox_items` + inboxReadsJoinClause + ` WHERE workspace_id = ?` + visClause + f.clause
	args = append(args, userID, workspaceID)
	args = append(args, visArgs...)
	args = append(args, f.args...)
	vals := make([]int, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := h.db.QueryRowContext(r.Context(), query, args...).Scan(ptrs...); err != nil {
		return err
	}
	for i, k := range inboxAttentionKeys {
		out.ByAttention[k] = vals[i]
	}
	for i, k := range inboxDeadlineKeys {
		out.ByDeadline[k] = vals[len(inboxAttentionKeys)+i]
	}
	return nil
}
