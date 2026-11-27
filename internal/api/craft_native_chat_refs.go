package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// chatArtifactRefsPage lists the artifacts a person attached to messages of a
// chat, oldest first: the lasting link from a conversation to its Sheets. It
// is read from artifact_message_refs, which only the trusted pre-send recorder
// writes; each row is shown only when the caller may see that page now, and a
// hidden row is neither counted nor hinted at.
type chatArtifactRefsPage struct {
	Refs       []chatArtifactRef `json:"refs"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type chatArtifactRef struct {
	MessageID   string `json:"message_id"`
	PageID      string `json:"page_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Application string `json:"application,omitempty"`
	CreatedAt   string `json:"created_at"`
	// Source is "message" (a message attached it; MessageID names it) or "upload" (a Sheet
	// opened from a file in this chat, before any message names it; MessageID is "").
	Source string `json:"source"`
}

// chatArtifactRefsScan bounds the rows one request reads, visible or not.
const chatArtifactRefsScan = 1000

type chatArtifactRefsCursor struct {
	At      string `json:"a"`
	Message string `json:"m"`
	Page    string `json:"p"`
}

func (r *Router) chatArtifactRefs(m *craftNativeHTTP) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if m == nil || m.pages == nil {
			replyError(w, 503, "artifact_refs_unavailable")
			return
		}
		chat := req.PathValue("chatId")
		// The same audience rule as the chat's other references: someone else's
		// chat, or a missing one, is the same 404.
		audience, err := r.entityRefAudience(req, chat)
		if err != nil {
			if errors.Is(err, errEntityAudienceDenied) {
				replyError(w, 404, "Chat not found")
			} else {
				replyError(w, 503, "artifact_refs_unavailable")
			}
			return
		}
		limit := 100
		if raw := req.URL.Query().Get("limit"); raw != "" {
			if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > 100 {
				replyError(w, 400, "limit_invalid")
				return
			}
		}
		var after chatArtifactRefsCursor
		if raw := req.URL.Query().Get("cursor"); raw != "" {
			data, decodeErr := base64.RawURLEncoding.DecodeString(raw)
			if decodeErr != nil || json.Unmarshal(data, &after) != nil || after.At == "" || after.Page == "" {
				replyError(w, 400, "cursor_invalid")
				return
			}
		}
		ctx := req.Context()
		tx, err := m.pages.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			replyError(w, 503, "artifact_refs_unavailable")
			return
		}
		defer tx.Rollback()
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		// A Sheet opened from a file in the chat is listed as an upload until a message of the
		// chat attaches it; then only as that message's.
		rows, err := tx.QueryContext(ctx, `SELECT message_id,page_id,created_at,source FROM (
 SELECT message_id,page_id,created_at,'message' AS source FROM artifact_message_refs WHERE chat_id=?1 AND workspace_id=?2
 UNION ALL
 SELECT '',u.page_id,u.created_at,'upload' FROM artifact_chat_refs u WHERE u.chat_id=?1 AND u.workspace_id=?2
  AND NOT EXISTS(SELECT 1 FROM artifact_message_refs mr WHERE mr.chat_id=u.chat_id AND mr.page_id=u.page_id))
 WHERE (created_at,message_id,page_id) > (?3,?4,?5)
 ORDER BY created_at,message_id,page_id LIMIT ?6`, chat, audience.WorkspaceID, after.At, after.Message, after.Page, chatArtifactRefsScan)
		if err != nil {
			replyError(w, 503, "artifact_refs_unavailable")
			return
		}
		var scanned []chatArtifactRef
		for rows.Next() {
			var ref chatArtifactRef
			if err = rows.Scan(&ref.MessageID, &ref.PageID, &ref.CreatedAt, &ref.Source); err != nil {
				break
			}
			scanned = append(scanned, ref)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			replyError(w, 503, "artifact_refs_unavailable")
			return
		}
		viewer, err := m.pages.loadViewer(ctx, audience.WorkspaceID, audience.UserID)
		if err != nil {
			replyError(w, 503, "artifact_refs_unavailable")
			return
		}
		page := chatArtifactRefsPage{Refs: []chatArtifactRef{}}
		var last *chatArtifactRef
		for i := range scanned {
			ref := scanned[i]
			if len(page.Refs) == limit {
				page.NextCursor = encodeChatArtifactRefsCursor(*last)
				break
			}
			last = &scanned[i]
			rec, loadErr := m.pages.loadPageForRead(ctx, audience.WorkspaceID, "@"+ref.PageID)
			if errors.Is(loadErr, sql.ErrNoRows) {
				continue
			}
			if loadErr != nil {
				replyError(w, 503, "artifact_refs_unavailable")
				return
			}
			panels, panelErr := m.pages.loadPanels(ctx, audience.WorkspaceID, rec.ID)
			if panelErr != nil {
				replyError(w, 503, "artifact_refs_unavailable")
				return
			}
			visible, seeErr := m.pages.canSeePage(ctx, audience.WorkspaceID, rec, panels, viewer)
			if seeErr != nil {
				replyError(w, 503, "artifact_refs_unavailable")
				return
			}
			if !visible {
				continue
			}
			ref.Slug, ref.Name, ref.Application = rec.Slug, rec.Name, rec.NativeCraftApplication
			page.Refs = append(page.Refs, ref)
		}
		// A full scan with rows left: carry on after the last row read, so hidden
		// rows never shorten a page into looking like the end.
		if page.NextCursor == "" && len(scanned) == chatArtifactRefsScan && last != nil {
			page.NextCursor = encodeChatArtifactRefsCursor(*last)
		}
		writeJSON(w, 200, page)
	}
}

func encodeChatArtifactRefsCursor(ref chatArtifactRef) string {
	data, _ := json.Marshal(chatArtifactRefsCursor{At: ref.CreatedAt, Message: ref.MessageID, Page: ref.PageID})
	return base64.RawURLEncoding.EncodeToString(data)
}
