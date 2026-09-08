package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Response DTOs are shared with the OpenAPI JSON-tag contract tests.
type workspaceConversationListResponse struct {
	Conversations []groupchat.Conversation `json:"conversations"`
	NextOffset    *int                     `json:"next_offset"`
}
type workspaceConversationMessagesResponse struct {
	Messages []groupchat.Message `json:"messages"`
	HasMore  bool                `json:"has_more"`
}
type workspaceConversationParticipantsResponse struct {
	Participants []groupchat.Member `json:"participants"`
}
type workspaceConversationAgentsResponse struct {
	Agents []groupchat.AgentMember `json:"agents"`
}
type workspaceConversationAgentJobsResponse struct {
	Jobs []groupchat.Job `json:"jobs"`
}

// WorkspaceConversationsHandler exposes agent-independent human conversations.
// Every store operation rechecks workspace membership and conversation access.
type WorkspaceConversationsHandler struct {
	store  *groupchat.Store
	logger *slog.Logger
	hub    *ws.Hub
}

func NewWorkspaceConversationsHandler(store *groupchat.Store, logger *slog.Logger) *WorkspaceConversationsHandler {
	return &WorkspaceConversationsHandler{store: store, logger: logger}
}
func (h *WorkspaceConversationsHandler) identity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	u := UserFromContext(r.Context())
	if u == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return "", "", false
	}
	ws := WorkspaceIDFromContext(r.Context())
	if ws == "" {
		replyError(w, http.StatusBadRequest, "workspace required")
		return "", "", false
	}
	return ws, u.ID, true
}
func (h *WorkspaceConversationsHandler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, groupchat.ErrForbidden):
		replyError(w, http.StatusNotFound, "conversation not found")
	case errors.Is(err, groupchat.ErrInvalid):
		replyError(w, http.StatusBadRequest, "invalid conversation request")
	case errors.Is(err, groupchat.ErrConflict):
		replyError(w, http.StatusConflict, "client_id already used with different content")
	default:
		h.logger.Error("workspace conversation request failed", "error", err)
		replyError(w, http.StatusInternalServerError, "internal")
	}
}
func conversationBody(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		replyError(w, http.StatusBadRequest, "expected one JSON object")
		return false
	}
	return true
}
func conversationPage(r *http.Request) (int64, int, error) {
	after := int64(0)
	limit := 100
	var err error
	if v := r.URL.Query().Get("after_sequence"); v != "" {
		after, err = strconv.ParseInt(v, 10, 64)
		if err != nil || after < 0 {
			return 0, 0, groupchat.ErrInvalid
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, groupchat.ErrInvalid
		}
	}
	return after, limit, nil
}
func (h *WorkspaceConversationsHandler) List(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	_, limit, err := conversationPage(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil || offset < 0 {
			h.fail(w, groupchat.ErrInvalid)
			return
		}
	}
	rows, err := h.store.ListPage(r.Context(), ws, u, limit, offset)
	if err != nil {
		h.fail(w, err)
		return
	}
	var next *int
	if len(rows) == limit {
		nextOffset := offset + limit
		next = &nextOffset
	}
	writeJSON(w, http.StatusOK, workspaceConversationListResponse{Conversations: rows, NextOffset: next})
}
func (h *WorkspaceConversationsHandler) Create(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in groupchat.CreateInput
	if !conversationBody(w, r, &in) {
		return
	}
	c, err := h.store.Create(r.Context(), ws, u, in)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, c.ID, "")
	writeJSON(w, http.StatusCreated, c)
}
func (h *WorkspaceConversationsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	c, err := h.store.Get(r.Context(), ws, u, r.PathValue("conversationId"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
func (h *WorkspaceConversationsHandler) Messages(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	after, limit, err := conversationPage(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	var rows []groupchat.Message
	if r.URL.Query().Has("after_sequence") {
		if r.URL.Query().Has("before_sequence") {
			h.fail(w, groupchat.ErrInvalid)
			return
		}
		rows, err = h.store.Messages(r.Context(), ws, u, r.PathValue("conversationId"), after, limit)
	} else {
		before := int64(0)
		if v := r.URL.Query().Get("before_sequence"); v != "" {
			before, err = strconv.ParseInt(v, 10, 64)
			if err != nil || before < 1 {
				h.fail(w, groupchat.ErrInvalid)
				return
			}
		}
		rows, err = h.store.MessagesBefore(r.Context(), ws, u, r.PathValue("conversationId"), before, limit)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceConversationMessagesResponse{Messages: rows, HasMore: len(rows) == limit})
}
func (h *WorkspaceConversationsHandler) Send(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in groupchat.SendInput
	if !conversationBody(w, r, &in) {
		return
	}
	m, duplicate, err := h.store.Send(r.Context(), ws, u, r.PathValue("conversationId"), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, m)
}
func (h *WorkspaceConversationsHandler) Read(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		LastReadSequence int64 `json:"last_read_sequence"`
	}
	if !conversationBody(w, r, &in) {
		return
	}
	if err := h.store.MarkRead(r.Context(), ws, u, r.PathValue("conversationId"), in.LastReadSequence); err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, r.PathValue("conversationId"), u)
	w.WriteHeader(http.StatusNoContent)
}
func (h *WorkspaceConversationsHandler) Members(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	rows, err := h.store.Members(r.Context(), ws, u, r.PathValue("conversationId"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceConversationParticipantsResponse{Participants: rows})
}
func (h *WorkspaceConversationsHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		UserID string `json:"user_id"`
	}
	if !conversationBody(w, r, &in) {
		return
	}
	if err := h.store.AddMember(r.Context(), ws, u, r.PathValue("conversationId"), in.UserID); err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, r.PathValue("conversationId"), in.UserID)
	w.WriteHeader(http.StatusNoContent)
}
func (h *WorkspaceConversationsHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	if err := h.store.RemoveMember(r.Context(), ws, u, r.PathValue("conversationId"), r.PathValue("userId")); err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, r.PathValue("conversationId"), r.PathValue("userId"))
	w.WriteHeader(http.StatusNoContent)
}

func (h *WorkspaceConversationsHandler) Agents(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	rows, err := h.store.Agents(r.Context(), ws, u, r.PathValue("conversationId"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceConversationAgentsResponse{Agents: rows})
}
func (h *WorkspaceConversationsHandler) AddAgent(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if !conversationBody(w, r, &in) {
		return
	}
	if err := h.store.AddAgent(r.Context(), ws, u, r.PathValue("conversationId"), in.AgentID); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *WorkspaceConversationsHandler) RemoveAgent(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	if err := h.store.RemoveAgent(r.Context(), ws, u, r.PathValue("conversationId"), r.PathValue("agentId")); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *WorkspaceConversationsHandler) AgentJobs(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	rows, err := h.store.Jobs(r.Context(), ws, u, r.PathValue("conversationId"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceConversationAgentJobsResponse{Jobs: rows})
}

// Advisory invalidations carry no private text. Durable messages have their
// own outbox; membership/read changes are also refreshed on reconnect/focus.
func (h *WorkspaceConversationsHandler) invalidateAudience(r *http.Request, workspaceID, userID, conversationID, extra string) {
	if h.hub == nil {
		return
	}
	ids := map[string]bool{}
	if extra != "" {
		ids[extra] = true
	}
	if r.URL.Path != "" && strings.HasSuffix(r.URL.Path, "/read") {
		ids[userID] = true
	} else {
		members, err := h.store.Members(r.Context(), workspaceID, userID, conversationID)
		if err != nil {
			h.logger.Warn("conversation audience refresh delayed", "error", err)
			return
		}
		for _, m := range members {
			ids[m.UserID] = true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	for id := range ids {
		for _, event := range []string{"conversation.updated", "inbox.updated"} {
			if err := h.hub.BroadcastChannelContext(ctx, "user", id, event, map[string]string{"conversation_id": conversationID}); err != nil {
				return
			}
		}
	}
}

func (h *WorkspaceConversationsHandler) Mute(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in struct {
		Muted *bool `json:"muted"`
	}
	if !conversationBody(w, r, &in) {
		return
	}
	if in.Muted == nil {
		h.fail(w, groupchat.ErrInvalid)
		return
	}
	if err := h.store.SetMuted(r.Context(), ws, u, r.PathValue("conversationId"), *in.Muted); err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, r.PathValue("conversationId"), u)
	w.WriteHeader(http.StatusNoContent)
}

// Direct discovers or creates a fixed two-human conversation. Identity comes
// exclusively from authentication; clients name only the other human.
func (h *WorkspaceConversationsHandler) Direct(w http.ResponseWriter, r *http.Request) {
	workspace, user, ok := h.identity(w, r)
	if !ok {
		return
	}
	var input struct {
		UserID string `json:"user_id"`
	}
	if !conversationBody(w, r, &input) {
		return
	}
	conversation, created, err := h.store.OpenDirect(r.Context(), workspace, user, input.UserID)
	if err != nil {
		h.fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	if created {
		h.invalidateAudience(r, workspace, user, conversation.ID, user)
	}
	writeJSON(w, status, conversation)
}
