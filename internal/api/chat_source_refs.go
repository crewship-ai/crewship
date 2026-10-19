package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/groupchat"
)

func validSourceProjection(r *http.Request) bool {
	values := r.URL.Query()["projection"]
	return len(values) == 1 && values[0] == conversation.SourceProjection
}

// chatSourceMessages must be called only after the existing human, role,
// workspace audience and restricted-history gates in ChatMessages.
func (h *ProxyHandler) chatSourceMessages(w http.ResponseWriter, r *http.Request) {
	if !validSourceProjection(r) || r.URL.Query().Has("offset") || r.URL.Query().Has("limit") {
		replyError(w, 400, "Invalid source projection; agent sessions use a bounded snapshot")
		return
	}
	resp, err := h.ipcGet(r.Context(), "/chats/"+url.PathEscape(r.PathValue("chatId"))+"/shared-messages")
	if err != nil {
		replyError(w, 503, "Source snapshot unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 413 {
		replyError(w, 413, "Source snapshot exceeds limits")
		return
	}
	if resp.StatusCode != 200 {
		replyError(w, 503, "Source snapshot unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if len(body) > 16<<20 {
		replyError(w, 413, "Source snapshot exceeds limits")
		return
	}
	var input struct {
		Messages *[]conversation.Message `json:"messages"`
	}
	if err != nil || json.Unmarshal(body, &input) != nil || input.Messages == nil {
		replyError(w, 503, "Source snapshot unavailable")
		return
	}
	if len(*input.Messages) > 1000 {
		replyError(w, 413, "Source snapshot exceeds limits")
		return
	}
	out, err := conversation.ProjectAgentSnapshot(conversation.ConversationRef{WorkspaceID: WorkspaceIDFromContext(r.Context()), SourceKind: conversation.SourceAgentSession, SourceID: r.PathValue("chatId")}, *input.Messages)
	if err != nil {
		replyError(w, 503, "Source snapshot unavailable")
		return
	}
	writeJSON(w, 200, out)
}

// writeRoomSourceMessages projects only rows already authorized by groupchat.Store.
func writeRoomSourceMessages(w http.ResponseWriter, r *http.Request, workspace, id string, rows []groupchat.Message, hasMore bool) {
	if !validSourceProjection(r) {
		replyError(w, 400, "Invalid source projection")
		return
	}
	ref := conversation.ConversationRef{WorkspaceID: workspace, SourceKind: conversation.SourceWorkspaceRoom, SourceID: id}
	if ref.Validate() != nil {
		replyError(w, 503, "Source messages unavailable")
		return
	}
	out := conversation.SourceMessagesResponse{ContractVersion: conversation.SourceContractVersion, ConversationRef: ref, Messages: make([]conversation.SourceMessage, 0, len(rows)), HasMore: &hasMore}
	seen := map[string]bool{}
	for _, m := range rows {
		if m.ConversationID != id || m.ID == "" || m.Sequence < 1 || seen[m.ID] {
			replyError(w, 503, "Source messages unavailable")
			return
		}
		seen[m.ID] = true
		seq := m.Sequence
		item := conversation.SourceMessage{MessageRef: &conversation.MessageRef{ConversationRef: ref, SourceMessageID: m.ID}, Order: conversation.SourceOrder{SourceSequence: &seq}, Content: m.Content, Parts: []conversation.Part{}}
		if m.AuthorUserID != "" {
			item.Author = &conversation.SourceAuthor{UserID: m.AuthorUserID}
		}
		if m.AuthorAgentID != "" && m.AuthorUserID == "" {
			item.Author = &conversation.SourceAuthor{AgentID: m.AuthorAgentID}
		}
		if m.CreatedAt != "" {
			ts := m.CreatedAt
			item.OccurredAt = &ts
		}
		if m.Kind == "message" && m.AuthorUserID != "" {
			role := "user"
			item.Role = &role
		}
		if m.Content != "" {
			item.Parts = append(item.Parts, conversation.Part{Type: "text", Content: m.Content})
		}
		out.Messages = append(out.Messages, item)
	}
	writeJSON(w, 200, out)
}
