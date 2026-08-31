package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
	"github.com/crewship-ai/crewship/internal/ws"
)

// InboxHandler serves the unified human-in-the-loop inbox. The list +
// state-transition endpoints read/write inbox_items directly; the
// rows themselves are inserted by the source-of-truth handlers
// (waitpoint create, escalation create, run-failure terminal) via the
// helpers in inbox_writer.go. This handler is strictly the read +
// state-flip surface the UI consumes.
type InboxHandler struct {
	db     *sql.DB
	logger *slog.Logger
	hub    *ws.Hub
}

// inboxVisibilityClause restricts inbox results to items targeted at
// either the workspace as a whole, the caller's user id, or a role the
// caller's role encompasses. Without this every workspace member could
// see items addressed to a specific OWNER (e.g. a routing-key escalation
// or a personal review request) — a real privacy / least-privilege gap.
// Returns the SQL fragment + the args to bind, in order.
//
// Role targeting is HIERARCHICAL: an item targeted at role X needs
// X-or-higher privilege to act on, so a caller sees it when their rank
// is >= X's rank. This is why an OWNER sees MANAGER-targeted escalations
// and failed-cron alerts (an earlier strict `target_role = caller_role`
// match hid every MANAGER item from the OWNER, who is the one person who
// should never miss them), while a MEMBER still can't see MANAGER items.
//
// All three handlers (List, UnreadCount, PatchState) call this so the
// predicate stays consistent across the surface.
func inboxVisibilityClause(userID, role string) (string, []interface{}) {
	// Target roles the caller can see = every role at or below the
	// caller's rank. roleRank[""] is 0, so an empty/unknown caller role
	// falls through to "untargeted + personal items only".
	callerRank := roleRank[role]
	args := []interface{}{userID}
	visible := make([]string, 0, len(roleRank))
	for name, rank := range roleRank {
		if rank > 0 && rank <= callerRank {
			visible = append(visible, name)
		}
	}

	clause := ` AND (
        (COALESCE(target_user_id, '') = '' AND COALESCE(target_role, '') = '')
        OR target_user_id = ?`
	if len(visible) > 0 {
		sort.Strings(visible) // deterministic SQL + arg order
		ph := make([]string, len(visible))
		for i, v := range visible {
			ph[i] = "?"
			args = append(args, v)
		}
		clause += `
        OR target_role IN (` + strings.Join(ph, ", ") + `)`
	}
	clause += `
    )`
	return clause, args
}

func NewInboxHandler(db *sql.DB, logger *slog.Logger, hub *ws.Hub) *InboxHandler {
	return &InboxHandler{db: db, logger: logger, hub: hub}
}

// inboxItemResponse is the wire shape for a single inbox row. We
// inline payload as a parsed map so the UI doesn't need to JSON.parse
// it client-side, and omit empty optional fields so consumers can
// switch on `routine_id != null`-style checks without first checking
// undefined.
type inboxItemResponse struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	Kind         string `json:"kind"`
	SourceID     string `json:"source_id"`
	TargetUserID string `json:"target_user_id,omitempty"`
	TargetRole   string `json:"target_role,omitempty"`
	// SourceMissing is set on the DETAIL read only, for the two source-managed
	// kinds. It answers the question the client used to guess at from the
	// payload: is there still a row anywhere that can decide this?
	//
	// It exists so the detail pane can offer a way out of a row whose source
	// was pruned without offering it on a live decision, where the PATCH is
	// refused and the button would just fail. Absent on list rows — the answer
	// costs a query per row and only the open row needs it.
	SourceMissing bool   `json:"source_missing,omitempty"`
	Title         string `json:"title"`
	BodyMD        string `json:"body_md,omitempty"`
	SenderType    string `json:"sender_type,omitempty"`
	SenderID      string `json:"sender_id,omitempty"`
	SenderName    string `json:"sender_name,omitempty"`
	// AvatarSeed / AvatarStyle are filled (post-query, via enrichAgentAvatars)
	// only when the sender is a real agent, so the inbox renders that agent's
	// actual avatar instead of a generic glyph. Blank for system / crew /
	// pipeline senders, which fall back to the kind glyph client-side.
	AvatarSeed  string `json:"avatar_seed,omitempty"`
	AvatarStyle string `json:"avatar_style,omitempty"`
	// AvatarURL points at the sender's stored avatar render (#1297) when it
	// has one. Empty means generate from the seed. Carried here so the inbox
	// shows the same face as the roster — without it, a generator upgrade
	// would redraw senders in the inbox while agent cards kept the stored
	// image.
	AvatarURL        string                 `json:"avatar_url,omitempty"`
	State            string                 `json:"state"`
	Priority         string                 `json:"priority"`
	Blocking         bool                   `json:"blocking"`
	Payload          map[string]interface{} `json:"payload,omitempty"`
	ReadAt           string                 `json:"read_at,omitempty"`
	ResolvedAt       string                 `json:"resolved_at,omitempty"`
	ResolvedByUserID string                 `json:"resolved_by_user_id,omitempty"`
	ResolvedAction   string                 `json:"resolved_action,omitempty"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`

	// The four-eyes rule as it will be applied to THIS row (issue #1574),
	// carrying the same field names and meaning the crew escalations list uses
	// (#1559) so the two surfaces cannot describe one rule differently.
	//
	// Filled at READ time by enrichEscalationFourEyes, never from Payload:
	// Payload is written when the escalation is raised, and both inputs to the
	// answer — the workspace toggle and the linked credential's tier — change
	// afterwards. A stored answer would go stale in the direction that matters,
	// leaving a one-click Approve on a row whose resolve now 403s.
	//
	// omitempty: only escalation rows can carry these, and every other kind in
	// the inbox would otherwise pay for them on every page.
	SecondApproverRequired    bool `json:"second_approver_required,omitempty"`
	SecondApproverByWorkspace bool `json:"second_approver_by_workspace,omitempty"`
	SecondApproverByTier      bool `json:"second_approver_by_tier,omitempty"`
	// SecurityLevelLabel is the linked credential's tier ("L4 · critical"), from
	// keeper's own table. Empty when the escalation has no credential behind it.
	SecurityLevelLabel string `json:"security_level_label,omitempty"`
	// Evidence is the facts block for a credential escalation — what the person
	// deciding needs beyond the model's argument for its own verdict. Detail view
	// only; see inbox_evidence.go for why it is not on the list.
	Evidence *inboxEvidence `json:"evidence,omitempty"`
}

// inboxListResponse keeps the count + cursor metadata next to the
// rows so the UI can render pagination + the bell badge from one
// fetch.
type inboxListResponse struct {
	Rows        []inboxItemResponse `json:"rows"`
	Count       int                 `json:"count"`
	UnreadCount int                 `json:"unread_count"`
	HasMore     bool                `json:"has_more"`
}

// List serves GET /api/v1/inbox. Filter by ?state=unread|read|resolved|all
// (default 'all' to drive Linear-Triage UX where resolved items stay
// visible-but-dimmed). ?kind= narrows by item type. ?limit defaults to
// 100, capped at 500. Sorted by created_at DESC so newest is at the
// top — same convention as Linear / GitHub Notifications.
func (h *InboxHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	state := r.URL.Query().Get("state")
	kind := r.URL.Query().Get("kind")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	q := strings.Builder{}
	q.WriteString(`SELECT id, workspace_id, kind, source_id,
		COALESCE(target_user_id, ''), COALESCE(target_role, ''),
		title, COALESCE(body_md, ''),
		COALESCE(sender_type, ''), COALESCE(sender_id, ''), COALESCE(sender_name, ''),
		state, priority, blocking, payload_json,
		COALESCE(read_at, ''), COALESCE(resolved_at, ''),
		COALESCE(resolved_by_user_id, ''), COALESCE(resolved_action, ''),
		created_at, updated_at
	FROM inbox_items WHERE workspace_id = ?`)
	args := []interface{}{workspaceID}

	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	q.WriteString(visClause)
	args = append(args, visArgs...)

	switch state {
	case "", "all":
		// no state predicate — every visible row
	case "active":
		// The Inbox view: everything not archived (unread + read).
		// Excluding resolved server-side means archived rows don't consume
		// the LIMIT window and silently push active items out of view.
		q.WriteString(" AND state != 'resolved'")
	case "unread", "read", "resolved":
		q.WriteString(" AND state = ?")
		args = append(args, state)
	default:
		replyError(w, http.StatusBadRequest, "invalid state")
		return
	}
	if kind != "" {
		q.WriteString(" AND kind = ?")
		args = append(args, kind)
	}
	// Fetch one extra row so clients can walk the entire feed without guessing
	// whether a full page was the final page. The id tie-breaker keeps OFFSET
	// pagination deterministic when several events share one timestamp.
	q.WriteString(" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?")
	args = append(args, limit+1, offset)

	rows, err := h.db.QueryContext(r.Context(), q.String(), args...)
	if err != nil {
		h.logger.Error("inbox list", "error", err)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	defer rows.Close()

	out := make([]inboxItemResponse, 0, limit+1)
	for rows.Next() {
		var item inboxItemResponse
		var blocking int
		var payloadJSON string
		if err := rows.Scan(
			&item.ID, &item.WorkspaceID, &item.Kind, &item.SourceID,
			&item.TargetUserID, &item.TargetRole,
			&item.Title, &item.BodyMD,
			&item.SenderType, &item.SenderID, &item.SenderName,
			&item.State, &item.Priority, &blocking, &payloadJSON,
			&item.ReadAt, &item.ResolvedAt,
			&item.ResolvedByUserID, &item.ResolvedAction,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			h.logger.Error("inbox scan", "error", err)
			continue
		}
		item.Blocking = blocking != 0
		if payloadJSON != "" {
			_ = json.Unmarshal([]byte(payloadJSON), &item.Payload)
		}
		out = append(out, item)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	h.enrichAgentAvatars(r.Context(), out)
	h.enrichEscalationFourEyes(r.Context(), workspaceID, out)

	// Bell badge fetched in the same response so the UI doesn't need
	// a second round-trip on every poll. Cheap because it's a partial-
	// indexed COUNT(*) on the workspace partition. Visibility predicate
	// kept in lockstep with the list query so a user's badge count
	// matches the rows they can actually see.
	var unreadCount int
	countQuery := `SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ?` + visClause + ` AND state = 'unread'`
	countArgs := append([]interface{}{workspaceID}, visArgs...)
	if err := h.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&unreadCount); err != nil {
		h.logger.Warn("inbox unread count", "error", err)
		unreadCount = 0
	}

	writeJSON(w, http.StatusOK, inboxListResponse{
		Rows:        out,
		Count:       len(out),
		UnreadCount: unreadCount,
		HasMore:     hasMore,
	})
}

// UnreadCount serves GET /api/v1/inbox/count — the bell-badge endpoint.
// Tiny payload, cheaper than List for the polling worker the top-bar
// bell uses.
func (h *InboxHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	args := append([]interface{}{workspaceID}, visArgs...)
	var n int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ?`+visClause+` AND state = 'unread'`,
		args...).Scan(&n); err != nil {
		h.logger.Warn("inbox unread count", "error", err)
		replyError(w, http.StatusInternalServerError, "count failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread_count": n})
}

// Get serves GET /api/v1/inbox/{id} — a single inbox item with its full
// body + payload, the context the list view omits. This is what gives
// the CLI (and any agent driving it) read parity with the web detail
// pane: `crewship inbox get <id>` can show the change plan, the escalation
// context, the run id, etc. Visibility is enforced exactly like List /
// PatchState — a cross-workspace or mis-targeted id 404s rather than
// leaking an item addressed to someone else.
func (h *InboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "id required")
		return
	}

	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	args := append([]interface{}{id, workspaceID}, visArgs...)
	var item inboxItemResponse
	var blocking int
	var payloadJSON string
	err := h.db.QueryRowContext(r.Context(), `SELECT id, workspace_id, kind, source_id,
		COALESCE(target_user_id, ''), COALESCE(target_role, ''),
		title, COALESCE(body_md, ''),
		COALESCE(sender_type, ''), COALESCE(sender_id, ''), COALESCE(sender_name, ''),
		state, priority, blocking, payload_json,
		COALESCE(read_at, ''), COALESCE(resolved_at, ''),
		COALESCE(resolved_by_user_id, ''), COALESCE(resolved_action, ''),
		created_at, updated_at
	FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
		args...).Scan(
		&item.ID, &item.WorkspaceID, &item.Kind, &item.SourceID,
		&item.TargetUserID, &item.TargetRole,
		&item.Title, &item.BodyMD,
		&item.SenderType, &item.SenderID, &item.SenderName,
		&item.State, &item.Priority, &blocking, &payloadJSON,
		&item.ReadAt, &item.ResolvedAt,
		&item.ResolvedByUserID, &item.ResolvedAction,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		h.logger.Error("inbox get", "error", err)
		replyError(w, http.StatusInternalServerError, "get failed")
		return
	}
	item.Blocking = blocking != 0
	if payloadJSON != "" {
		_ = json.Unmarshal([]byte(payloadJSON), &item.Payload)
	}
	batch := []inboxItemResponse{item}
	h.enrichAgentAvatars(r.Context(), batch)
	h.enrichEscalationFourEyes(r.Context(), workspaceID, batch)
	// Detail only. Two indexed queries for one item; the list deliberately does
	// not pay this per row.
	h.enrichKeeperEvidence(r.Context(), workspaceID, &batch[0])
	// Same probes the PATCH guard uses, so the button the client renders and
	// the answer the server will give cannot disagree.
	switch batch[0].Kind {
	case "waitpoint":
		batch[0].SourceMissing = !waitpointHasBackingRow(r.Context(), h.db, workspaceID, batch[0].SourceID)
	case "escalation":
		batch[0].SourceMissing = !escalationHasBackingRow(r.Context(), h.db, batch[0].SourceID)
	}
	writeJSON(w, http.StatusOK, batch[0])
}

// enrichAgentAvatars fills avatar_seed / avatar_style on rows whose sender is
// a real agent, so the inbox can render the agent's actual avatar (the same
// DiceBear seed/style the agent card uses) instead of a generic glyph. One
// batched lookup keyed by sender_id; non-agent senders (system / crew /
// pipeline) and unknown ids are left blank and fall back to the kind glyph
// client-side. Best-effort: a lookup error logs and leaves avatars blank
// rather than failing the list.
func (h *InboxHandler) enrichAgentAvatars(ctx context.Context, rows []inboxItemResponse) {
	ids := make([]interface{}, 0)
	seen := make(map[string]bool)
	for i := range rows {
		if rows[i].SenderType == "agent" && rows[i].SenderID != "" && !seen[rows[i].SenderID] {
			seen[rows[i].SenderID] = true
			ids = append(ids, rows[i].SenderID)
		}
	}
	if len(ids) == 0 {
		return
	}
	ph := sqlPlaceholders(len(ids))
	r, err := h.db.QueryContext(ctx,
		`SELECT id, COALESCE(avatar_seed, ''), COALESCE(avatar_style, ''), COALESCE(avatar_svg_hash, '')
		 FROM agents WHERE id IN (`+ph+`)`,
		ids...)
	if err != nil {
		h.logger.Warn("inbox avatar enrich", "error", err)
		return
	}
	defer r.Close()
	type avatar struct{ seed, style, svgHash string }
	byID := make(map[string]avatar, len(ids))
	for r.Next() {
		var id, seed, style, svgHash string
		if err := r.Scan(&id, &seed, &style, &svgHash); err != nil {
			continue
		}
		byID[id] = avatar{seed, style, svgHash}
	}
	for i := range rows {
		if a, ok := byID[rows[i].SenderID]; ok {
			rows[i].AvatarSeed = a.seed
			rows[i].AvatarStyle = a.style
			if u := agentAvatarURL(rows[i].SenderID, a.svgHash, WorkspaceIDFromContext(ctx)); u != nil {
				rows[i].AvatarURL = *u
			}
		}
	}
}

// enrichEscalationFourEyes fills the four-eyes fields on kind=escalation rows,
// mirroring ResolveEscalation's own reasoning exactly (issue #1574):
//
//   - CREDENTIAL escalations only.
//   - The rule compares the approver against the agent's recorded owner, so an
//     agent with no owner (legacy pre-v99 row) cannot have it enforced — and a
//     row claiming otherwise would threaten a 403 that will not happen, which
//     is worse than saying nothing.
//   - The workspace toggle opts every tier in; the credential's own tier forces
//     it on the top tier regardless. Either is sufficient.
//
// Why here and not on the stored payload, which is where the inbox gets
// everything else it renders: the payload is written when the escalation is
// raised, and BOTH inputs move afterwards — the workspace toggle is a switch an
// admin flips, and a credential can be re-tiered from L2 to L4 long after the
// row landed. A snapshot would keep offering an unguarded one-click Approve for
// a credential somebody has since marked critical.
//
// Cost is bounded per page, not per item: one batched join keyed by source_id,
// and the governance row read once — and only when at least one row could
// actually be subject to the rule. Best-effort, like enrichAgentAvatars: a
// lookup failure leaves the fields unset (the row claims nothing) rather than
// failing the list, because the enforcement is server-side either way and the
// operator meeting the 403 is strictly better than an empty inbox.
func (h *InboxHandler) enrichEscalationFourEyes(ctx context.Context, workspaceID string, rows []inboxItemResponse) {
	ids := make([]interface{}, 0)
	seen := make(map[string]bool)
	for i := range rows {
		if rows[i].Kind == "escalation" && rows[i].SourceID != "" && !seen[rows[i].SourceID] {
			seen[rows[i].SourceID] = true
			ids = append(ids, rows[i].SourceID)
		}
	}
	if len(ids) == 0 {
		return
	}

	// LEFT JOIN on credentials, not JOIN: a CREDENTIAL escalation may carry no
	// credential row (the legacy flow where the human supplies the secret), and
	// a credential_id can dangle. Both mean "no tier to read", which is what
	// ResolveEscalation concludes from sql.ErrNoRows.
	//
	// The agents JOIN is deliberately inner: no agent row means no recorded
	// owner to compare against, which is the case where the rule cannot be
	// enforced — such a row drops out of this map and claims nothing.
	// Two sources, one shape. An escalation-backed row's source_id is an
	// escalations id; a KEEPER credential escalation's is a keeper_requests id and
	// has no escalations row at all — keeper_request.go writes the inbox item
	// directly (the same fact humanInboxSQL in the eval corpus relies on).
	//
	// Reading only the first table is what left the keeper card silent: #1574 put
	// the four-eyes notice in the inbox precisely so an operator would not meet
	// the 403 cold, and then #1671 gave that card an Approve button while the
	// notice was still never fed for it. An OWNER pressed Approve on an L4 request
	// and got a refusal the card had given no hint of.
	//
	// keeper_requests is NOT only credential requests: request_type also admits
	// skill_review, behavior, memory_health and negative_learning, and five sites
	// in keeper_phase2.go write inbox escalations for those with source_id = the
	// keeper request id and target_role=MANAGER — legitimately, since none of them
	// names a credential. Matching every keeper request would tell a skill review
	// it needs a second approver, which it never will: four-eyes is a rule about
	// credential escalations and there is no credential here to be refused over. A
	// warning that cannot come true is worse than none, because it teaches the
	// operator to skip the one that can.
	//
	// So the filter is `access`/`execute` — the two types that name a credential —
	// and the reported type is a literal because for those two it is always
	// CREDENTIAL. The agents JOIN stays inner for the same reason as above: no
	// recorded owner means nothing to compare an approver against, so the row drops
	// out and claims nothing.
	args := append([]interface{}{workspaceID}, ids...)
	args = append(args, workspaceID)
	args = append(args, ids...)
	q, err := h.db.QueryContext(ctx, `
		SELECT e.id, e.type, COALESCE(a.created_by_user_id, ''), c.security_level
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		LEFT JOIN credentials c ON c.id = e.credential_id AND c.workspace_id = e.workspace_id
		WHERE e.workspace_id = ? AND e.id IN (`+sqlPlaceholders(len(ids))+`)
		UNION ALL
		SELECT kr.id, 'CREDENTIAL', COALESCE(a.created_by_user_id, ''), c.security_level
		FROM keeper_requests kr
		JOIN agents a ON a.id = kr.requesting_agent_id
		LEFT JOIN credentials c ON c.id = kr.credential_id AND c.workspace_id = a.workspace_id
		WHERE a.workspace_id = ? AND kr.request_type IN ('access','execute')
		  AND kr.id IN (`+sqlPlaceholders(len(ids))+`)`,
		args...)
	if err != nil {
		h.logger.Warn("inbox four-eyes enrich", "error", err)
		return
	}
	defer q.Close()

	type escFacts struct {
		escType       string
		initiatorUser string
		securityLevel sql.NullInt64
	}
	bySource := make(map[string]escFacts, len(ids))
	for q.Next() {
		var id string
		var f escFacts
		if err := q.Scan(&id, &f.escType, &f.initiatorUser, &f.securityLevel); err != nil {
			continue
		}
		bySource[id] = f
	}
	if err := q.Err(); err != nil {
		h.logger.Warn("inbox four-eyes enrich", "error", err)
		return
	}

	// The governance row is read once for the whole page, and only if some row
	// on it could be subject to the rule at all.
	needsGovernance := false
	for _, f := range bySource {
		if f.escType == "CREDENTIAL" && f.initiatorUser != "" {
			needsGovernance = true
			break
		}
	}
	var gov governance.Settings
	if needsGovernance {
		gov = governance.Resolve(ctx, h.db, h.logger, workspaceID)
	}

	for i := range rows {
		// Kind first: another kind's source_id is a waitpoint token or a run id,
		// and one colliding with an escalation id must not inherit its answer.
		if rows[i].Kind != "escalation" {
			continue
		}
		f, ok := bySource[rows[i].SourceID]
		if !ok {
			continue
		}
		if f.securityLevel.Valid {
			rows[i].SecurityLevelLabel = keeper.SecurityLevel(f.securityLevel.Int64).Label()
		}
		if f.escType != "CREDENTIAL" || f.initiatorUser == "" {
			continue
		}
		rows[i].SecondApproverByWorkspace = gov.RequireSecondApprover
		if f.securityLevel.Valid {
			rows[i].SecondApproverByTier = keeper.SecurityLevel(f.securityLevel.Int64).Tier().SecondApprover
		}
		rows[i].SecondApproverRequired = rows[i].SecondApproverByWorkspace || rows[i].SecondApproverByTier
	}
}

// escalationHasBackingRow reports whether an inbox escalation's source_id
// points at a real row in the escalations table (i.e. it's resolvable at
// /escalations/{id}/resolve). Keeper-synthetic advisories (Skill review,
// memory health) use a synthetic source_id with no backing row — those have
// no source endpoint, so the inbox row itself is the only handle and may be
// dismissed directly. On a query error we stay conservative (treat as backed)
// so the source-managed guard is never weakened by a transient failure.
// Both probes take the package's rowQuerier (issue_handler.go) so the PATCH
// guard, inside a transaction, and the detail read, outside one, ask the same
// question with the same code.
//
// waitpointHasBackingRow reports whether a waitpoint inbox row still has a
// source that can decide it. Three shapes, because three producers write this
// kind and each keys it differently:
//
//   - a pipeline waitpoint, keyed by its token (pipeline/waitpoints.go)
//   - the agent a guided hire staged, while it is still PENDING_REVIEW
//     (agents_hire.go writes the inbox row with SourceID = agentID)
//   - an autonomy hold, whose SourceID is a CREW, AGENT or MISSION id and
//     whose live decision is the pending approvals_queue row that names it
//     (internal_autonomy_gate.go). Missing this arm is not academic: a crew or
//     mission hold matches neither table above, so it would have looked
//     orphaned and become dismissable on the inbox row while its approval was
//     still pending — handing out exactly the free resolve this guard exists
//     to refuse.
//
// This is the same question escalationHasBackingRow asks, and it is asked for
// the same reason: the guard below exists so a client cannot flip an inbox row
// and leave a live decision un-made. When there is no live decision left — the
// run was pruned, the hire was swept, the row was seeded — refusing is not
// protecting anything, it is a trap with no way out. The escalation half of
// that trap was closed; the waitpoint half was not, and a waitpoint is the one
// kind PATCH refuses outright, so those rows sat in Needs action forever.
func waitpointHasBackingRow(ctx context.Context, tx rowQuerier, workspaceID, sourceID string) bool {
	if sourceID == "" {
		return false
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM pipeline_waitpoints WHERE token = ? AND workspace_id = ?
		UNION ALL
		SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
		UNION ALL
		SELECT 1 FROM approvals_queue
		 WHERE workspace_id = ? AND status = 'pending'
		   AND json_extract(payload, '$.target_id') = ?
	)`, sourceID, workspaceID, sourceID, workspaceID, workspaceID, sourceID).Scan(&exists); err != nil {
		// Unreadable means "assume it is backed" — the safe direction is to
		// keep forcing the source endpoint, never to hand out a free resolve.
		return true
	}
	return exists == 1
}

func escalationHasBackingRow(ctx context.Context, tx rowQuerier, sourceID string) bool {
	if sourceID == "" {
		return false
	}
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM escalations WHERE id = ?)`, sourceID).Scan(&exists); err != nil {
		return true
	}
	return exists == 1
}

// PatchState handles PATCH /api/v1/inbox/{id} to flip an item's state
// between unread/read/resolved. Resolved transitions also accept a
// `resolved_action` discriminator (approved / rejected / retried /
// cancelled) so the audit trail records what the user did, not just
// that they did something.
func (h *InboxHandler) PatchState(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "id required")
		return
	}

	var body struct {
		State          string `json:"state"`
		ResolvedAction string `json:"resolved_action,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.State != "unread" && body.State != "read" && body.State != "resolved" {
		replyError(w, http.StatusBadRequest, "state must be unread|read|resolved")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Verify the row exists, in this workspace, AND visible to this
	// caller before flipping. A cross-workspace id should 404 rather
	// than silently no-op; an item targeted at another user / role
	// must also 404 so a workspace member can't flip a row addressed
	// to a specific OWNER.
	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	lookupArgs := append([]interface{}{id, workspaceID}, visArgs...)
	var existing, kind, sourceID string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id, kind, source_id FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
		lookupArgs...).Scan(&existing, &kind, &sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		h.logger.Error("inbox patch lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	// Source-managed kinds (waitpoint / escalation) must keep their
	// inbox state in sync with the authoritative source row. The inbox
	// PATCH is fine for "read" (the inbox row tracks its own visibility
	// marker) but "resolved" and "unread" would desync — the user
	// expects the inbox flip to also approve the waitpoint / close the
	// escalation, and it doesn't. Force callers through the proper
	// source endpoints (/pipelines/waitpoints/{token}/approve, etc.)
	// for those transitions.
	//
	// failed_run is deliberately NOT in this set: a terminally-failed
	// run has no source "resolve" endpoint — the inbox item is the only
	// artifact, so resolving/dismissing it on the inbox row IS the
	// correct semantics. (Retry is a separate re-fire via
	// /pipelines/{slug}/run; it creates a NEW run rather than resolving
	// the source.) Keeping failed_run here made its Cancel/Retry inbox
	// actions 409 and the items pile up with no way to clear them.
	// Generic kinds (message) can flip freely too.
	if kind == "waitpoint" || kind == "escalation" {
		if body.State != "read" {
			// Exception: a row whose SOURCE no longer exists. A keeper-synthetic
			// escalation (Skill review, memory health) carries kind=escalation
			// but has NO backing escalations row — its source_id is a synthetic
			// key — so there is no /escalations/{id}/resolve to drive it. The
			// same is true of a waitpoint whose pipeline_waitpoints token is
			// gone, or whose staged hire was already swept: the source endpoint
			// answers 404 and PATCH answered 409, so the row could never leave
			// Needs action and never reach History.
			//
			// Blocking inbox-resolve traps those forever; allow the operator to
			// dismiss them on the inbox row instead. Rows that ARE still backed
			// keep 409ing → decide at the source, which is the whole point of
			// the guard.
			sourceLess := body.State == "resolved" && ((kind == "escalation" && !escalationHasBackingRow(r.Context(), tx, sourceID)) ||
				(kind == "waitpoint" && !waitpointHasBackingRow(r.Context(), tx, workspaceID, sourceID)))
			if !sourceLess {
				writeJSON(w, http.StatusConflict, map[string]string{
					"error": "use the source endpoint for this kind (e.g. /pipelines/waitpoints/{token}/approve) — inbox PATCH only supports 'read' for source-managed items",
					"kind":  kind,
				})
				return
			}
		}
	}

	switch body.State {
	case "read":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'read',
			    read_at = COALESCE(read_at, ?),
			    read_by_user_id = COALESCE(read_by_user_id, ?),
			    updated_at = ?
			WHERE id = ?`,
			now, user.ID, now, id)
	case "unread":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'unread',
			    read_at = NULL,
			    read_by_user_id = NULL,
			    resolved_at = NULL,
			    resolved_by_user_id = NULL,
			    resolved_action = NULL,
			    updated_at = ?
			WHERE id = ?`,
			now, id)
	case "resolved":
		_, err = tx.ExecContext(r.Context(), `
			UPDATE inbox_items
			SET state = 'resolved',
			    resolved_at = ?,
			    resolved_by_user_id = ?,
			    resolved_action = ?,
			    updated_at = ?
			WHERE id = ?`,
			now, user.ID, body.ResolvedAction, now, id)
	}
	if err != nil {
		h.logger.Error("inbox patch state", "error", err)
		replyError(w, http.StatusInternalServerError, "update failed")
		return
	}

	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusInternalServerError, "commit failed")
		return
	}

	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"id":    id,
			"state": body.State,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": body.State})
}

// bulkMaxIDs caps a single bulk request. The tree-grouped UI resolves a
// whole routine/crew group at once; 500 matches the list LIMIT ceiling
// so "select everything visible, resolve" can't exceed what one page
// loaded.
const bulkMaxIDs = 500

// BulkPatchState handles POST /api/v1/inbox/bulk — apply ONE state
// transition to many items at once, the engine behind the tree-grouped
// UI's "resolve all under this routine / crew" action. The body carries
// an explicit id list (the client already has the rows loaded and knows
// exactly which group it's clearing); the server re-checks workspace +
// visibility per id so a caller can't flip rows addressed to someone
// else by stuffing ids into the array.
//
// The same source-managed guard as PatchState applies, but PARTIALLY:
// waitpoint/escalation rows that can't take the requested non-read
// state are SKIPPED rather than failing the whole batch, so a mixed
// selection still clears everything it legitimately can (failed_run +
// message resolve freely; see PatchState for why failed_run isn't
// source-managed). The response reports updated vs skipped counts so
// the UI can say "22 resolved, 3 need the source endpoint".
func (h *InboxHandler) BulkPatchState(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}

	var body struct {
		IDs            []string `json:"ids"`
		State          string   `json:"state"`
		ResolvedAction string   `json:"resolved_action,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.State != "unread" && body.State != "read" && body.State != "resolved" {
		replyError(w, http.StatusBadRequest, "state must be unread|read|resolved")
		return
	}
	if len(body.IDs) == 0 {
		replyError(w, http.StatusBadRequest, "ids required")
		return
	}
	if len(body.IDs) > bulkMaxIDs {
		replyError(w, http.StatusBadRequest, "too many ids (max 500)")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	visClause, visArgs := inboxVisibilityClause(user.ID, role)

	updated := 0
	skipped := make([]string, 0)
	notFound := 0
	seen := make(map[string]bool, len(body.IDs))
	for _, id := range body.IDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		var existing, kind, sourceID string
		var blocking int
		lookupArgs := append([]interface{}{id, workspaceID}, visArgs...)
		err = tx.QueryRowContext(r.Context(),
			`SELECT id, kind, source_id, blocking FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
			lookupArgs...).Scan(&existing, &kind, &sourceID, &blocking)
		if errors.Is(err, sql.ErrNoRows) {
			notFound++
			continue
		}
		if err != nil {
			h.logger.Error("inbox bulk lookup", "error", err)
			replyError(w, http.StatusInternalServerError, "lookup failed")
			return
		}

		// Decision-item protection. Bulk MUST NOT silently close anything
		// an agent is waiting on a human to decide — one misclick on
		// "Resolve all" could otherwise approve/dismiss dozens of pending
		// requests. So on a resolve (not 'read', which is harmless) we
		// SKIP, never fail:
		//   - source-managed kinds (waitpoint/escalation): their real
		//     state lives in the source table, not the inbox row; and
		//   - any blocking=true row regardless of kind: "blocking" means
		//     "needs explicit human action".
		// Non-blocking message/failed_run still clear. The client warns
		// the user before calling; this is the server-side backstop.
		// Same source-less exception as PatchState: a keeper-synthetic
		// escalation with no backing escalations row has no source endpoint,
		// so dismissing it on the inbox row is the only way to clear it.
		// Those resolve even under bulk; real escalations/waitpoints/blocking
		// rows are still skipped.
		// Same predicate as PatchState — a bulk archive must not refuse rows
		// the single-row path accepts, or "select all → archive" silently
		// leaves the orphans behind.
		sourceLess := (kind == "escalation" && !escalationHasBackingRow(r.Context(), tx, sourceID)) ||
			(kind == "waitpoint" && !waitpointHasBackingRow(r.Context(), tx, workspaceID, sourceID))
		if body.State == "resolved" && !sourceLess && (kind == "waitpoint" || kind == "escalation" || blocking != 0) {
			skipped = append(skipped, id)
			continue
		}
		// 'unread' on source-managed kinds would desync the source row —
		// only 'read' is allowed for those. Skip (not fail) here too.
		if body.State == "unread" && (kind == "waitpoint" || kind == "escalation") {
			skipped = append(skipped, id)
			continue
		}

		switch body.State {
		case "read":
			_, err = tx.ExecContext(r.Context(), `
				UPDATE inbox_items
				SET state = 'read',
				    read_at = COALESCE(read_at, ?),
				    read_by_user_id = COALESCE(read_by_user_id, ?),
				    updated_at = ?
				WHERE id = ?`,
				now, user.ID, now, id)
		case "unread":
			_, err = tx.ExecContext(r.Context(), `
				UPDATE inbox_items
				SET state = 'unread', read_at = NULL, read_by_user_id = NULL,
				    resolved_at = NULL, resolved_by_user_id = NULL, resolved_action = NULL,
				    updated_at = ?
				WHERE id = ?`,
				now, id)
		case "resolved":
			_, err = tx.ExecContext(r.Context(), `
				UPDATE inbox_items
				SET state = 'resolved', resolved_at = ?, resolved_by_user_id = ?,
				    resolved_action = ?, updated_at = ?
				WHERE id = ?`,
				now, user.ID, body.ResolvedAction, now, id)
		}
		if err != nil {
			h.logger.Error("inbox bulk update", "error", err, "id", id)
			replyError(w, http.StatusInternalServerError, "update failed")
			return
		}
		updated++
	}

	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusInternalServerError, "commit failed")
		return
	}

	// One broadcast for the whole batch — the client invalidates its
	// inbox queries on any inbox.updated, so a single event repaints the
	// list + badge without flooding the socket with N messages.
	if h.hub != nil && updated > 0 {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"bulk":  "true",
			"state": body.State,
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated":     updated,
		"skipped":     len(skipped),
		"skipped_ids": skipped,
		"not_found":   notFound,
		"state":       body.State,
	})
}

// Purge handles DELETE /api/v1/inbox — HARD-delete inbox rows for the context
// workspace. This is a teardown/reset primitive (seed --nuke's full wipe, or an
// operator clearing accumulated failed-run spam), NOT a per-user action: unlike
// List/PatchState it deliberately does NOT apply the per-user visibility clause
// — it clears the whole workspace partition regardless of who each row was
// targeted at. Because that's destructive and cross-user, it's gated on the
// "manage" role (OWNER/ADMIN) rather than any workspace member (the bulk
// state-flip endpoint is member-open because it only touches rows the caller can
// already see; a purge ignores that boundary, so it needs the stronger gate).
//
// Optional ?kind=failed_run scopes the delete to one kind — the common case:
// drop the failed-run notifications a broken scheduled routine piled up without
// touching pending waitpoints/escalations. With no kind, every row in the
// workspace is deleted. Returns {"deleted": N}.
func (h *InboxHandler) Purge(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}

	query := `DELETE FROM inbox_items WHERE workspace_id = ?`
	args := []interface{}{workspaceID}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		switch kind {
		case "waitpoint", "escalation", "failed_run", "message":
			query += ` AND kind = ?`
			args = append(args, kind)
		default:
			replyError(w, http.StatusBadRequest, "kind must be waitpoint|escalation|failed_run|message")
			return
		}
	}

	res, err := h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("inbox purge", "error", err)
		replyError(w, http.StatusInternalServerError, "purge failed")
		return
	}
	deleted, _ := res.RowsAffected()

	// One broadcast so any open inbox repaints its list + bell badge.
	if h.hub != nil && deleted > 0 {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"purge": "true",
		})
	}

	writeJSON(w, http.StatusOK, map[string]int64{"deleted": deleted})
}
