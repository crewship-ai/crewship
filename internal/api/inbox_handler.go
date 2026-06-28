package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	ID               string                 `json:"id"`
	WorkspaceID      string                 `json:"workspace_id"`
	Kind             string                 `json:"kind"`
	SourceID         string                 `json:"source_id"`
	TargetUserID     string                 `json:"target_user_id,omitempty"`
	TargetRole       string                 `json:"target_role,omitempty"`
	Title            string                 `json:"title"`
	BodyMD           string                 `json:"body_md,omitempty"`
	SenderType       string                 `json:"sender_type,omitempty"`
	SenderID         string                 `json:"sender_id,omitempty"`
	SenderName       string                 `json:"sender_name,omitempty"`
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
}

// inboxListResponse keeps the count + cursor metadata next to the
// rows so the UI can render pagination + the bell badge from one
// fetch.
type inboxListResponse struct {
	Rows        []inboxItemResponse `json:"rows"`
	Count       int                 `json:"count"`
	UnreadCount int                 `json:"unread_count"`
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
	q.WriteString(" ORDER BY created_at DESC LIMIT ?")
	args = append(args, limit)

	rows, err := h.db.QueryContext(r.Context(), q.String(), args...)
	if err != nil {
		h.logger.Error("inbox list", "error", err)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	defer rows.Close()

	out := make([]inboxItemResponse, 0, limit)
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
	writeJSON(w, http.StatusOK, item)
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
	var existing, kind string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id, kind FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
		lookupArgs...).Scan(&existing, &kind)
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
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "use the source endpoint for this kind (e.g. /pipelines/waitpoints/{token}/approve) — inbox PATCH only supports 'read' for source-managed items",
				"kind":  kind,
			})
			return
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

		var existing, kind string
		var blocking int
		lookupArgs := append([]interface{}{id, workspaceID}, visArgs...)
		err = tx.QueryRowContext(r.Context(),
			`SELECT id, kind, blocking FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
			lookupArgs...).Scan(&existing, &kind, &blocking)
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
		if body.State == "resolved" && (kind == "waitpoint" || kind == "escalation" || blocking != 0) {
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
