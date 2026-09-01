package api

// Renaming a chat session — PATCH /api/v1/agents/{agentId}/chats/{chatId}
// (PRD chat-as-a-primary-surface, Step 2).
//
// `chats.title` has been in the schema since the first migration
// (migrate_consts_v01_init.go) and ListChats has always returned it, but
// nothing ever wrote it: every session in every list read "Untitled session",
// which makes a list of conversations unusable at any size. This is the write.
//
// It is a plain rename, deliberately: the client derives the first title from
// the opening message and PATCHes it, and a person can correct it afterwards.
// Server-side (model-generated) titles were considered and cut — see §7 O4 of
// the PRD.

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// chatTitleMaxRunes caps a session title at 200 RUNES.
//
// The column is TEXT, so this is a product decision rather than a storage one:
// a title is a label in a sidebar row, a command-palette hit and a notification
// subject, and past ~200 characters it stops being a label and becomes a
// paragraph that every surface has to truncate anyway. 200 leaves generous room
// above the ~60 characters the client derives from the first message, so a
// human correcting a title is never fighting the limit.
//
// Runes, not bytes: a byte cap would silently give a Czech or Japanese author a
// third of the title an English one gets. Same reasoning as
// maxSuggestedPromptLength in agents_suggested_prompts.go.
const chatTitleMaxRunes = 200

// zeroWidthJoiner is U+200D. It is category Cf like the invisible characters
// the normaliser drops, but it is the glue inside emoji sequences (a family
// emoji is three people joined by two of these), so it is exempted by name.
const zeroWidthJoiner = '\u200D'

// chatResponse is one chat session on the wire. ListChats returns an array of
// these and UpdateChat returns exactly one, so the frontend can replace a row
// in place after a rename without refetching the list.
//
// Title, EndedAt and Origin are pointers because the columns are nullable and
// "not set" is meaningful (an untitled session, a live session, a session whose
// origin predates the column) — a "" would be indistinguishable from a title
// somebody actually cleared.
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
	// Kind is mode+origin already decided, so a client never has to
	// re-derive the partition and can never disagree with the `kind`
	// filter it just used. Always one of ChatKind — never null, never
	// empty: `direct` is the catch-all (see chat_kinds.go).
	Kind ChatKind `json:"kind"`
}

// errChatTitleEmpty and errChatTitleTooLong are the two ways a title is
// refused. Named so the handler's 400s read as one decision.
var (
	errChatTitleEmpty   = errors.New("title must not be empty")
	errChatTitleTooLong = errors.New("title is too long")
)

// normalizeChatTitle turns what a person (or a client deriving from a first
// message) sent into the single line that gets stored.
//
//   - every whitespace run — including the newlines and tabs a paste brings
//     along — collapses to ONE space, because a title is one line by
//     definition and a stored newline would break every list that renders it;
//   - control characters (Cc) and invisible format characters (Cf) are dropped.
//     Cf is what carries the RIGHT-TO-LEFT OVERRIDE trick that makes
//     "safe<U+202E>txt.exe" render as "safeexe.txt" in a sidebar, and the
//     zero-width padding that makes two different titles look identical. The
//     ZERO WIDTH JOINER is the one exception: it is load-bearing inside emoji
//     sequences, and stripping it turns a family emoji into three people;
//   - the result is trimmed and capped.
//
// What it deliberately does NOT do is escape or strip markup. The title is user
// content; escaping is the job of whatever renders it, and doing it here would
// corrupt legitimate titles ("<Draft> plan", "Tom & Jerry") while giving a
// false sense that the stored value is safe HTML. It is not — it is text, and
// every consumer must treat it as text.
func normalizeChatTitle(raw string) (string, error) {
	var b strings.Builder
	b.Grow(len(raw))
	pendingSpace := false
	for _, r := range raw {
		switch {
		case unicode.IsSpace(r):
			// Fold, don't emit — a leading run is dropped by the check below
			// and an interior run becomes exactly one space.
			pendingSpace = b.Len() > 0
			continue
		case r == zeroWidthJoiner: // part of the text, not decoration — keep it
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r):
			continue
		}
		if pendingSpace {
			b.WriteRune(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}

	title := b.String()
	if title == "" {
		return "", errChatTitleEmpty
	}
	if utf8.RuneCountInString(title) > chatTitleMaxRunes {
		return "", errChatTitleTooLong
	}
	return title, nil
}

// UpdateChat renames a chat session.
// PATCH /api/v1/agents/{agentId}/chats/{chatId}
//
// Body: {"title": "..."}. The response is one chat row in exactly the shape
// ListChats returns its elements, so a client can splice the result straight
// into the list it already holds instead of refetching.
//
// Scope and gate mirror the sibling mutating chat routes: the workspace comes
// from the request context and never from the body, a chat that is not this
// agent's (or not this workspace's) 404s rather than leaking its existence, and
// the caller must be the chat's creator or able to edit the agent.
func (h *AgentHandler) UpdateChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}

	// A *string separates the three ways "title" can be missing — absent key,
	// explicit null, wrong type — from an empty string the caller really sent.
	// All four are refused, but only a present string reaches the normaliser.
	var body struct {
		Title *string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request: title must be a string")
		return
	}
	if body.Title == nil {
		replyError(w, http.StatusBadRequest, "title is required")
		return
	}
	title, err := normalizeChatTitle(*body.Title)
	if err != nil {
		switch {
		case errors.Is(err, errChatTitleTooLong):
			replyError(w, http.StatusBadRequest,
				"title is too long (max "+strconv.Itoa(chatTitleMaxRunes)+" characters)")
		default:
			replyError(w, http.StatusBadRequest,
				"title must not be empty once whitespace and control characters are removed")
		}
		return
	}

	// Scope check mirrors MarkChatRead / DeleteChat: a cross-tenant or
	// mis-nested id 404s without leaking existence, and without writing.
	var createdBy sql.NullString
	err = h.db.QueryRowContext(r.Context(),
		`SELECT created_by FROM chats WHERE id = ? AND agent_id = ? AND workspace_id = ?`,
		chatID, agentID, workspaceID).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "update chat lookup", err)
		return
	}

	// Same creator-or-agent-editor gate DeleteChat runs, and for the same
	// reason: the route registers roleSelf → scopeSelf, so the route-level
	// scope gate never fires and the handler is the authorization. The creator
	// arm consumes no resource capability (naming a session you started); the
	// other arm rewrites a label every member of the workspace reads, so it
	// re-imposes agents:write on top of canEditAgent — otherwise a CLI token
	// narrowed to an unrelated scope, held by an OWNER/ADMIN, could retitle any
	// chat in the workspace.
	if !createdBy.Valid || createdBy.String != user.ID {
		role := RoleFromContext(r.Context())
		if !canScope(r.Context(), "agents:write") {
			replyForbidden(w, h.logger, user.ID, role, "chat.rename", "chat:"+chatID)
			return
		}
		ok, err := canEditAgent(r.Context(), h.db, user.ID, role, agentID)
		if err != nil {
			replyInternalError(w, h.logger, "update chat gate", err)
			return
		}
		if !ok {
			replyForbidden(w, h.logger, user.ID, role, "chat.rename", "chat:"+chatID)
			return
		}
	}

	// updated_at moves; last_activity_at deliberately does NOT. ListChats
	// orders by last_activity_at, so bumping it here would shove a thread to
	// the top of the sidebar merely because someone fixed its name.
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE chats SET title = ?, updated_at = ? WHERE id = ?`,
		title, isoMillisNow(), chatID); err != nil {
		replyInternalError(w, h.logger, "update chat title", err)
		return
	}

	row, err := h.chatRowByID(r, chatID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload renamed chat", err)
		return
	}

	// Sidebars open elsewhere in the workspace repaint from this instead of
	// polling. Nothing new is exposed: ListChats already serves every chat of
	// an agent to any workspace member.
	h.broadcastAgentEvent("chat_renamed", workspaceID, map[string]string{
		"agent_id": agentID,
		"chat_id":  chatID,
		"title":    title,
	})

	writeJSON(w, http.StatusOK, row)
}

// chatRowByID re-reads one chat in the exact projection ListChats uses,
// including the caller's unread_count, so the PATCH response and one element of
// the list are the same object to a client.
func (h *AgentHandler) chatRowByID(r *http.Request, chatID, userID string) (*chatResponse, error) {
	var c chatResponse
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.agent_id, c.workspace_id, c.title, c.mode, c.status,
			c.message_count, c.started_at, c.ended_at, c.created_at, c.origin,
			COALESCE(c.last_activity_at,
				strftime('%Y-%m-%dT%H:%M:%fZ', c.started_at),
				c.started_at) AS last_activity_at
		FROM chats c
		WHERE c.id = ?
	`, chatID).Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
		&c.Mode, &c.Status, &c.MessageCount,
		&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.Origin,
		&c.LastActivityAt); err != nil {
		return nil, err
	}
	c.Kind = ChatKindOf(c.Mode, derefOr(c.Origin, ""))
	unread, err := h.chatUnreadCounts(r.Context(), []string{chatID}, userID)
	if err != nil {
		return nil, err
	}
	c.UnreadCount = unread[c.ID]
	return &c, nil
}

// derefOr reads a nullable string column's Go form. `origin` is a *string
// because "never set" is meaningful on the wire; the classifier only cares
// about the value, and NULL classifies the same as an unrecognised one.
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}
