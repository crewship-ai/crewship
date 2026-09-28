package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chatshare"
	"github.com/crewship-ai/crewship/internal/conversation"
)

const (
	defaultChatShareTTL = 24 * time.Hour
	maxChatShareTTL     = 7 * 24 * time.Hour
	maxSharedChatBody   = 32 << 20
)

// ChatSharesHandler exposes one read-only transcript through a revocable,
// chat-bound capability. The public reader deliberately does not run AuthMiddleware.
type ChatSharesHandler struct {
	store  *chatshare.Store
	proxy  *ProxyHandler
	logger *slog.Logger
}

type chatShareListResponse struct {
	Shares []chatshare.Grant `json:"shares"`
}

type chatShareMessagesResponse struct {
	Messages []sharedChatText `json:"messages"`
}

func NewChatSharesHandler(db *sql.DB, proxy *ProxyHandler, logger *slog.Logger) *ChatSharesHandler {
	return &ChatSharesHandler{store: chatshare.NewStore(db), proxy: proxy, logger: logger}
}

func chatShareManagementScope(r *http.Request) (string, string, string, string) {
	user := UserFromContext(r.Context())
	if user == nil {
		return "", "", "", ""
	}
	return WorkspaceIDFromContext(r.Context()), r.PathValue("agentId"), r.PathValue("chatId"), user.ID
}

func chatShareStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chatshare.ErrDenied):
		replyError(w, http.StatusNotFound, "chat share not found")
	case errors.Is(err, chatshare.ErrInvalid):
		replyError(w, http.StatusBadRequest, "invalid chat share request")
	default:
		replyError(w, http.StatusInternalServerError, "chat share operation failed")
	}
}

// Create mints a token only for the chat creator or current workspace admin.
// The plaintext token appears once in this response and is never listed.
func (h *ChatSharesHandler) Create(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// A scoped CLI token that can read an agent must not mint a transferable
	// transcript credential. Session and legacy unscoped tokens retain the
	// creator/admin rule enforced by the store.
	if !canScope(r.Context(), "agents:write") {
		replyError(w, http.StatusForbidden, "agents:write scope required")
		return
	}
	ws, agent, chat, actor := chatShareManagementScope(r)
	if ws == "" || agent == "" || chat == "" || actor == "" {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	var body struct {
		TTLSeconds int64 `json:"ttl_seconds"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ttl := defaultChatShareTTL
	if body.TTLSeconds != 0 {
		if body.TTLSeconds < 0 || body.TTLSeconds > int64(maxChatShareTTL/time.Second) {
			replyError(w, http.StatusBadRequest, "ttl_seconds must be between 1 and 604800")
			return
		}
		ttl = time.Duration(body.TTLSeconds) * time.Second
	}
	grant, token, err := h.store.Create(r.Context(), ws, agent, chat, actor, ttl)
	if err != nil {
		chatShareStoreError(w, err)
		return
	}
	if h.logger != nil {
		h.logger.Info("chat share created", "share_id", grant.ID, "workspace_id", ws, "agent_id", agent, "chat_id", chat, "issuer_user_id", actor, "expires_at", grant.ExpiresAt)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"share": grant, "token": token})
}

func (h *ChatSharesHandler) List(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !canScope(r.Context(), "agents:read") {
		replyError(w, http.StatusForbidden, "agents:read scope required")
		return
	}
	ws, agent, chat, actor := chatShareManagementScope(r)
	if ws == "" || agent == "" || chat == "" || actor == "" {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	grants, err := h.store.List(r.Context(), ws, agent, chat, actor)
	if err != nil {
		chatShareStoreError(w, err)
		return
	}
	if grants == nil {
		grants = []chatshare.Grant{}
	}
	writeJSON(w, http.StatusOK, chatShareListResponse{Shares: grants})
}

func (h *ChatSharesHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !canScope(r.Context(), "agents:write") {
		replyError(w, http.StatusForbidden, "agents:write scope required")
		return
	}
	ws, agent, chat, actor := chatShareManagementScope(r)
	if ws == "" || agent == "" || chat == "" || actor == "" {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	if err := h.store.Revoke(r.Context(), ws, agent, chat, r.PathValue("shareId"), actor); err != nil {
		chatShareStoreError(w, err)
		return
	}
	if h.logger != nil {
		h.logger.Info("chat share revoked", "share_id", r.PathValue("shareId"), "workspace_id", ws, "agent_id", agent, "chat_id", chat, "actor_user_id", actor)
	}
	w.WriteHeader(http.StatusNoContent)
}

type sharedChatText struct {
	ID        string            `json:"id"`
	Role      conversation.Role `json:"role"`
	Content   string            `json:"content"`
	CreatedAt time.Time         `json:"created_at"`
}

// Messages accepts only the share bearer token. It never takes a caller-chosen
// chat id, and projects only user/assistant text from the IPC transcript.
func (h *ChatSharesHandler) Messages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Query-string tokens leak through logs, history and referrers. Reject the
	// entire query to keep this capability out of URL-based transport.
	if r.URL.RawQuery != "" {
		replyError(w, http.StatusBadRequest, "query parameters are not supported")
		return
	}
	shareID := r.PathValue("shareId")
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer cshr_") || strings.ContainsAny(strings.TrimPrefix(auth[0], "Bearer "), "\r\n, \t") {
		replyError(w, http.StatusUnauthorized, "share token required")
		return
	}
	token := strings.TrimPrefix(auth[0], "Bearer ")
	grant, err := h.store.Validate(r.Context(), shareID, token)
	if err != nil {
		if errors.Is(err, chatshare.ErrDenied) || errors.Is(err, chatshare.ErrInvalid) {
			replyError(w, http.StatusNotFound, "chat share not found")
		} else {
			replyError(w, http.StatusServiceUnavailable, "chat share unavailable")
		}
		return
	}
	if h.proxy == nil {
		replyError(w, http.StatusServiceUnavailable, "chat history unavailable")
		return
	}
	resp, err := h.proxy.ipcGet(r.Context(), "/chats/"+url.PathEscape(grant.ChatID)+"/shared-messages")
	if err != nil {
		replyError(w, http.StatusBadGateway, "chat history unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		replyError(w, http.StatusRequestEntityTooLarge, "chat exceeds sharing limit (16 MiB or 1000 messages)")
		return
	}
	if resp.StatusCode != http.StatusOK {
		replyError(w, http.StatusBadGateway, "chat history unavailable")
		return
	}
	var payload struct {
		Messages []conversation.Message `json:"messages"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSharedChatBody+1))
	if len(raw) > maxSharedChatBody {
		replyError(w, http.StatusRequestEntityTooLarge, "chat exceeds sharing response limit")
		return
	}
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		replyError(w, http.StatusBadGateway, "chat history unavailable")
		return
	}
	result := make([]sharedChatText, 0, len(payload.Messages))
	for _, msg := range payload.Messages {
		if msg.Role != conversation.RoleUser && msg.Role != conversation.RoleAssistant {
			continue
		}
		// For structured turns, only text parts are exposed. Flattened Content
		// is used solely by legacy turns with no parts.
		content := msg.Content
		if len(msg.Parts) > 0 {
			var b strings.Builder
			for _, part := range msg.Parts {
				if part.Type == "text" {
					b.WriteString(part.Content)
				}
			}
			content = b.String()
		}
		if content == "" {
			continue
		}
		result = append(result, sharedChatText{ID: msg.ID, Role: msg.Role, Content: content, CreatedAt: msg.Timestamp})
	}
	writeJSON(w, http.StatusOK, chatShareMessagesResponse{Messages: result})
}
