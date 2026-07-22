package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// isoMillisNow returns the current UTC time in the fixed-width
// millisecond-ISO form ("2026-07-02T10:00:00.000Z"). This matches
// SQLite's strftime('%Y-%m-%dT%H:%M:%fZ','now') and, unlike
// RFC3339Nano (which strips trailing zeros), stays lexically
// comparable against conversation_messages.ts and
// chats.last_activity_at — the unread computation is a pure string
// comparison in SQL.
func isoMillisNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// chatReplyInboxSourceID is the inbox_items.source_id dedupe key for the
// "your agent replied" notification of one (chat, user) pair. Shared
// between the chatnotify writer and MarkChatRead (which clears it) — keep
// in sync with internal/chatnotify.
func chatReplyInboxSourceID(chatID, userID string) string {
	return "chat_reply_" + chatID + "_" + userID
}

// ListChats returns all chat sessions for a given agent, ordered by most
// recent activity. Each row carries the requesting user's unread_count
// (messages not authored by them appended after their read cursor) and
// last_activity_at so the Sessions sidebar can order + badge sessions.
// GET /api/v1/agents/{agentId}/chats
func (h *AgentHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}

	// last_activity_at falls back to started_at (normalised through
	// strftime so legacy space-separated rows compare with ISO rows).
	//
	// Two statements on purpose (#1255 item 6): first the page of chats,
	// then ONE batched unread aggregate over exactly the page's ids.
	// The ORDER BY is a COALESCE expression, so it cannot ride
	// idx_chats_agent_activity — EXPLAIN shows USE TEMP B-TREE FOR ORDER
	// BY, which materialises the full projection for every chat of the
	// agent BEFORE the LIMIT prunes to 100. With unread_count inline
	// (the pre-#1255 correlated-subquery shape), that meant the two
	// unread index probes
	//
	//	SEARCH m  USING INDEX idx_conversation_messages_session (session_id=? AND ts>?)
	//	SEARCH rc USING INDEX sqlite_autoindex_chat_read_cursors_1 (user_id=? AND chat_id=?)
	//
	// ran once per EXISTING chat, not per RETURNED chat. Splitting moves
	// the unread work after the LIMIT: O(page ≤ 100) evaluations instead
	// of O(total chats), while the page query's per-row cost drops to a
	// plain projection + sort.
	//
	// Do NOT re-merge this into a single LEFT JOIN + GROUP BY statement:
	// that shape was built, measured, and rejected in #1262 (~25% slower
	// — same index probes per chat, still pre-LIMIT, plus an extra temp
	// b-tree materialising one row per unread message). The batched
	// second query below keeps the per-chat plan of the correlated
	// subquery (same two SEARCHes, driven from the chats pk for each id
	// in the page) but is bounded by the page.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.agent_id, c.workspace_id, c.title, c.mode, c.status,
			c.message_count, c.started_at, c.ended_at, c.created_at, c.origin,
			COALESCE(c.last_activity_at,
				strftime('%Y-%m-%dT%H:%M:%fZ', c.started_at),
				c.started_at) AS last_activity_at
		FROM chats c
		WHERE c.agent_id = ? AND c.workspace_id = ?
		ORDER BY last_activity_at DESC
		LIMIT 100
	`, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "list agent chats", err)
		return
	}
	defer rows.Close()

	type chatResponse struct {
		ID             string  `json:"id"`
		AgentID        string  `json:"agent_id"`
		WorkspaceID    string  `json:"workspace_id"`
		Title          *string `json:"title"`
		Mode           string  `json:"mode"`
		Status         string  `json:"status"`
		MessageCount   int     `json:"message_count"`
		StartedAt      string  `json:"started_at"`
		EndedAt        *string `json:"ended_at"`
		CreatedAt      string  `json:"created_at"`
		Origin         *string `json:"origin"`
		LastActivityAt string  `json:"last_activity_at"`
		UnreadCount    int     `json:"unread_count"`
	}

	var result []chatResponse
	for rows.Next() {
		var c chatResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
			&c.Mode, &c.Status, &c.MessageCount,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.Origin,
			&c.LastActivityAt); err != nil {
			replyInternalError(w, h.logger, "scan chat", err)
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (chats)", err)
		return
	}

	if len(result) > 0 {
		ids := make([]string, len(result))
		for i := range result {
			ids[i] = result[i].ID
		}
		unread, err := h.chatUnreadCounts(r.Context(), ids, userID)
		if err != nil {
			replyInternalError(w, h.logger, "batch unread counts", err)
			return
		}
		for i := range result {
			result[i].UnreadCount = unread[result[i].ID]
		}
	}

	if result == nil {
		result = []chatResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// chatUnreadCounts returns the requesting user's unread_count for each
// chat on the page, keyed by chat id (missing key == 0). One statement
// for the whole page — the #1255 item 6 batched aggregate.
//
// unread_count counts messages the user hasn't seen and didn't write:
// the author guard treats authorless user-role rows (legacy,
// scheduler-injected) as "mine" so they never inflate the badge;
// assistant/system rows always count. No cursor row means "never
// opened" — everything counts (migration v130 backfills a cursor for
// pre-existing chats so upgrades ship quiet). The predicate and the
// cursor COALESCE must stay byte-identical to the pre-split correlated
// shape — TestListChats_UnreadMatchesCorrelatedReference diffs this
// handler against a frozen copy of that query, and
// TestListChats_UnreadProjectionBehaviourLock pins the absolute values.
//
// The aggregate is driven from chats (pk lookups over the IN list)
// rather than from conversation_messages so the plan per chat is the
// same pair of index probes the correlated subquery used — cursor point
// lookup once per chat, then an index range scan of the chat's messages
// with ts as a seek bound:
//
//	SEARCH c USING COVERING INDEX sqlite_autoindex_chats_1 (id=?)
//	SEARCH m USING INDEX idx_conversation_messages_session (session_id=? AND ts>?) LEFT-JOIN
//	CORRELATED SCALAR SUBQUERY 1
//	  SEARCH rc USING INDEX sqlite_autoindex_chat_read_cursors_1 (user_id=? AND chat_id=?)
//
// (no temp b-tree at all — the GROUP BY rides the pk-ordered IN scan).
//
// Anchoring on conversation_messages instead would demote ts to a
// post-join filter (the cursor join can't feed a seek bound once m is
// the outer table), degrading each chat to a full-history scan.
func (h *AgentHandler) chatUnreadCounts(ctx context.Context, chatIDs []string, userID string) (map[string]int, error) {
	args := make([]any, 0, len(chatIDs)+2)
	args = append(args, userID, userID)
	placeholders := make([]string, len(chatIDs))
	for i, id := range chatIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT c.id, COUNT(m.id)
		FROM chats c
		LEFT JOIN conversation_messages m ON m.session_id = c.id
			AND NOT (m.role = 'user' AND (m.author_user_id IS NULL OR m.author_user_id = ?))
			AND m.ts > COALESCE((SELECT rc.last_read_at FROM chat_read_cursors rc
			                     WHERE rc.chat_id = c.id AND rc.user_id = ?), '')
		WHERE c.id IN (`+strings.Join(placeholders, ",")+`)
		GROUP BY c.id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	unread := make(map[string]int, len(chatIDs))
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		unread[id] = n
	}
	return unread, rows.Err()
}

// CreateChat starts a new chat session with the specified agent.
// POST /api/v1/agents/{agentId}/chats
func (h *AgentHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	userID := UserFromContext(r.Context()).ID

	var body struct {
		SessionID string `json:"session_id"`
		// Origin distinguishes how the session was started: "UI" (chat
		// page in the browser), "CLI" (`crewship run`), "WEBHOOK",
		// "CRON", "AGENT" (agent-to-agent assignment). The
		// SessionsSidebar renders a colored chip per origin. Unknown
		// or empty values are stored as NULL → no chip shown.
		Origin string `json:"origin"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	chatID := body.SessionID
	if chatID == "" {
		chatID = generateCUID()
	}

	// Whitelist allowed origin values; anything else becomes NULL so a
	// rogue caller can't shove arbitrary text into a UI-rendered chip.
	var origin sql.NullString
	switch body.Origin {
	case "UI", "CLI", "WEBHOOK", "CRON", "AGENT":
		origin = sql.NullString{String: body.Origin, Valid: true}
	}

	// Check agent exists
	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "check agent exists", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Atomic upsert: insert only if agent is still active (prevents TOCTOU with soft-delete)
	_, err = h.db.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO chats (id, agent_id, workspace_id, created_by, status, origin, last_activity_at)
		 SELECT ?, ?, ?, ?, 'ACTIVE', ?, ?
		 WHERE EXISTS (SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL)`,
		chatID, agentID, workspaceID, userID, origin, isoMillisNow(), agentID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "create chat", err)
		return
	}

	// Check outcome: either inserted, already existed (IGNORE), or agent was deleted
	var ownerAgentID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT agent_id FROM chats WHERE id = ?", chatID).Scan(&ownerAgentID); err != nil {
		if err == sql.ErrNoRows {
			// No row: agent was deleted between preflight and INSERT (WHERE EXISTS failed)
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		replyInternalError(w, h.logger, "verify chat owner", err)
		return
	}
	if ownerAgentID != agentID {
		replyError(w, http.StatusConflict, "Chat belongs to a different agent")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": chatID})
}

// MarkChatRead advances the caller's read cursor on a chat to "now",
// zeroing its unread_count in ListChats, and clears the paired "agent
// replied" inbox notification for this (user, chat) so the bell badge
// and the sidebar badge stay in lockstep.
// PUT /api/v1/agents/{agentId}/chats/{chatId}/read
func (h *AgentHandler) MarkChatRead(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}

	// Scope check: the chat must belong to this agent AND this
	// workspace. A cross-tenant or mis-nested id 404s (don't leak
	// existence, don't write a cursor).
	var one int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM chats WHERE id = ? AND agent_id = ? AND workspace_id = ?`,
		chatID, agentID, workspaceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "mark chat read lookup", err)
		return
	}

	now := isoMillisNow()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, chat_id) DO UPDATE SET last_read_at = excluded.last_read_at`,
		user.ID, chatID, now); err != nil {
		replyInternalError(w, h.logger, "mark chat read upsert", err)
		return
	}

	// Clear the reply notification this cursor supersedes. Best-effort:
	// the cursor is the durable state; a failed inbox flip only leaves a
	// stale bell item the user can dismiss manually.
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE inbox_items
		SET state = 'read',
		    read_at = COALESCE(read_at, ?),
		    read_by_user_id = COALESCE(read_by_user_id, ?),
		    updated_at = ?
		WHERE workspace_id = ? AND kind = 'message' AND source_id = ? AND state = 'unread'`,
		now, user.ID, now, workspaceID, chatReplyInboxSourceID(chatID, user.ID))
	if err != nil {
		h.logger.Warn("mark chat read: inbox clear", "error", err, "chat_id", chatID)
	} else if n, _ := res.RowsAffected(); n > 0 {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"source": "chat_read",
			"chat":   chatID,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"chat_id":      chatID,
		"last_read_at": now,
	})
}

// DeleteChat removes a chat session and its conversation history.
// DELETE /api/v1/agents/{agentId}/chats/{chatId}
//
// Added for #998: routine iterate's one-shot grader/optimizer calls used
// to strand two orphan chats per round in the agent's session sidebar.
// The gate is creator-or-agent-editor: the chat's creator may always
// remove their own session (a MEMBER cleaning up after themselves), and
// anyone who can edit the agent (canEditAgent — OWNER/ADMIN, MANAGER
// with ownership/crew elevation) may remove any of its chats.
func (h *AgentHandler) DeleteChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}

	// Scope check mirrors MarkChatRead: cross-tenant / mis-nested ids 404
	// without leaking existence. LEFT JOIN agents so we also learn the
	// agent's slug + crew_id for the #1148 attachment-blob cleanup below —
	// LEFT (not INNER) so a chat whose agent was since soft/hard-deleted
	// still deletes cleanly (slug/crew_id come back NULL → cleanup no-ops).
	var createdBy, agentSlug, agentCrewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT c.created_by, a.slug, a.crew_id
		   FROM chats c LEFT JOIN agents a ON a.id = c.agent_id
		  WHERE c.id = ? AND c.agent_id = ? AND c.workspace_id = ?`,
		chatID, agentID, workspaceID).Scan(&createdBy, &agentSlug, &agentCrewID)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "delete chat lookup", err)
		return
	}

	if !createdBy.Valid || createdBy.String != user.ID {
		role := RoleFromContext(r.Context())
		// The route registers roleSelf → scopeSelf (#1074), so the
		// route-level scope gate never runs here. That exemption is for the
		// creator arm above — self-cleanup of a chat the token itself
		// created consumes no resource capability. This arm deletes ANOTHER
		// principal's chat via canEditAgent, which passes unconditionally
		// for OWNER/ADMIN — so a leaked CLI token narrowed to an unrelated
		// scope must NOT reach it. Re-impose the scope scopeForRoute used to
		// demand for this pattern before the roleSelf change.
		if !canScope(r.Context(), "agents:write") {
			replyForbidden(w, h.logger, user.ID, role, "chat.delete", "chat:"+chatID)
			return
		}
		ok, err := canEditAgent(r.Context(), h.db, user.ID, role, agentID)
		if err != nil {
			replyInternalError(w, h.logger, "delete chat gate", err)
			return
		}
		if !ok {
			replyForbidden(w, h.logger, user.ID, role, "chat.delete", "chat:"+chatID)
			return
		}
	}

	// Delegation records pin the chat: assignments.chat_id is a NOT NULL
	// FK with no ON DELETE clause, so deleting a chat a lead ever
	// delegated from would either 500 on the constraint or (with a
	// cascade) silently destroy the delegation audit trail. Refuse with
	// a clear 409 instead — such chats are operational history, not
	// clutter.
	var assignmentCount int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM assignments WHERE chat_id = ?`, chatID).Scan(&assignmentCount); err != nil {
		replyInternalError(w, h.logger, "delete chat: assignment check", err)
		return
	}
	if assignmentCount > 0 {
		replyError(w, http.StatusConflict,
			"Chat has delegation records (assignments) and cannot be deleted — it is part of the audit trail")
		return
	}

	// Chat + messages + read cursors go together — a partial delete would
	// leave orphaned history rows no surface can reach. The per-user
	// "agent replied" inbox items go too (source_id = chat_reply_<chat>_<user>,
	// see chatReplyInboxSourceID): leaving them would keep stale unread
	// bells whose "Open chat" deep link now 404s.
	//
	// Deliberately NOT touched (#1074): peer_conversations.chat_id and
	// escalations.chat_id are TEXT NOT NULL with no FK to chats — they record
	// operational history (a delegated question, a credential escalation) that
	// outlives the chat it originated in, so we leave them dangling rather than
	// delete audit rows or (impossibly, they are NOT NULL) null the pointer.
	// This is safe because every reader treats chat_id as an OPAQUE field: no
	// query JOINs chats through it or dereferences it, so a pointer to a gone
	// chat degrades gracefully (an "open source chat" deep link may 404, same
	// as any deleted resource). Attachment blobs under
	// <storage-root>/<crew>/<agent>/attachments/<chatId>/ are unlinked
	// best-effort AFTER the tx commits (#1148) — see cleanupChatAttachments.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "delete chat begin tx", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`DELETE FROM conversation_messages WHERE session_id = ?`, []any{chatID}},
		{`DELETE FROM chat_read_cursors WHERE chat_id = ?`, []any{chatID}},
		{`DELETE FROM inbox_items WHERE workspace_id = ? AND kind = 'message' AND source_id LIKE ? ESCAPE '\'`,
			[]any{workspaceID, `chat\_reply\_` + escapeLikeWildcards(chatID) + `\_%`}},
		{`DELETE FROM chats WHERE id = ?`, []any{chatID}},
	} {
		if _, err := tx.ExecContext(r.Context(), stmt.q, stmt.args...); err != nil {
			h.logger.Error("delete chat", "error", err, "chat_id", chatID)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "delete chat commit", err)
		return
	}

	// #1148: the DB rows are gone; now unlink the chat's attachment blobs.
	// Best-effort AFTER commit — a storage error is logged but must not
	// fail the delete (the operator's chat is already gone, and the
	// residual is a disk-space leak, not a correctness bug).
	h.cleanupChatAttachments(agentCrewID, agentSlug, chatID)

	h.broadcastAgentEvent("inbox.updated", workspaceID, map[string]string{
		"source": "chat_deleted",
		"chat":   chatID,
	})

	h.broadcastAgentEvent("chat_deleted", workspaceID, map[string]string{
		"agent_id": agentID,
		"chat_id":  chatID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// cleanupChatAttachments removes the on-disk attachment directory for a
// deleted chat: <storagePath>/<crewID>/<slug>/attachments/<chatId>/,
// the exact tree AgentChatAttachment writes to via the IPC files/save
// path. Best-effort: every early-return / error is a silent no-op or a
// WARN — callers have already committed the DB delete and a blob
// residual must never turn a successful delete into a 500.
//
// The three id segments come from trusted sources (crew_id + slug are
// DB columns; chatID is the already-scope-checked path value), but each
// is still re-validated against safeIDPattern before it is joined onto
// the host path. That keeps CodeQL's path-injection check satisfied and
// adds defense-in-depth: a corrupted row or a future non-CUID id scheme
// can never produce a "../" segment that escapes storagePath and unlinks
// an unrelated tree.
//
// TODO(#1148): this unlinks via os.RemoveAll on the local storagePath,
// which only cleans up the default localfs backend. provider.StorageProvider
// already defines a Delete(ctx, path) site, so once a non-local backend
// (e.g. an S3 StorageProvider) is wired, these blobs would leak — route the
// cleanup through StorageProvider (walk the attachments prefix + Delete each
// object, since the interface has no recursive RemoveAll) instead of the
// filesystem directly. Tracked for the storage-backend generalization.
func (h *AgentHandler) cleanupChatAttachments(crewID, slug sql.NullString, chatID string) {
	if h.storagePath == "" || !crewID.Valid || !slug.Valid {
		return // unwired storage, or an agent with no crew — no attachments possible
	}
	if !safeIDPattern.MatchString(crewID.String) ||
		!safeIDPattern.MatchString(slug.String) ||
		!safeIDPattern.MatchString(chatID) {
		h.logger.Warn("skip chat attachment cleanup: unsafe id segment",
			"crew_id", crewID.String, "slug", slug.String, "chat_id", chatID)
		return
	}
	dir := filepath.Join(h.storagePath, crewID.String, slug.String, "attachments", chatID)
	if err := os.RemoveAll(dir); err != nil {
		// RemoveAll returns nil when the dir never existed, so this is a
		// genuine unlink failure (perms, I/O) — worth a breadcrumb, not a
		// failed request.
		h.logger.Warn("chat attachment cleanup failed", "dir", dir, "error", err)
	}
}

// ListRuns returns all execution runs for a given agent, ordered by most recent first.
// GET /api/v1/agents/{agentId}/runs
//
// Reads from journal_entries (unified-journal Phase E). Up to 100 most
// recent runs scoped to the workspace + agent_id.
func (h *AgentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	aggregated, _, err := journal.ListRuns(r.Context(), h.db, journal.RunsQuery{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Limit:       100,
	})
	if err != nil {
		replyInternalError(w, h.logger, "list agent runs", err)
		return
	}

	// Per-agent endpoint doesn't enrich with crew/agent names — caller
	// already knows the agent context. Convert directly.
	result := make([]runResponse, 0, len(aggregated))
	for _, ar := range aggregated {
		resp := runResponse{
			ID:           ar.ID,
			AgentID:      ar.AgentID,
			WorkspaceID:  ar.WorkspaceID,
			TriggerType:  ar.TriggerType,
			Status:       string(ar.Status),
			ErrorMessage: stringPtrOrNil(ar.ErrorMessage),
			ExitCode:     ar.ExitCode,
			CreatedAt:    formatRFC3339(ar.CreatedAt),
		}
		if ar.ChatID != "" {
			c := ar.ChatID
			resp.ChatID = &c
		}
		if ar.TriggeredBy != "" {
			t := ar.TriggeredBy
			resp.TriggeredBy = &t
		}
		if !ar.StartedAt.IsZero() {
			s := formatRFC3339(ar.StartedAt)
			resp.StartedAt = &s
		}
		if ar.FinishedAt != nil && !ar.FinishedAt.IsZero() {
			f := formatRFC3339(*ar.FinishedAt)
			resp.FinishedAt = &f
		}
		if ar.Metadata != nil {
			if b, jerr := json.Marshal(ar.Metadata); jerr == nil {
				resp.Metadata = b
			}
		}
		result = append(result, resp)
	}
	writeJSON(w, http.StatusOK, result)
}
