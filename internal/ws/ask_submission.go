package ws

import (
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/askforms"
)

// This id only correlates a private transport reply. It grants no authority;
// the bridge independently validates the entire submission after resolution.
func askSubmissionID(metadata map[string]any) string {
	raw, ok := metadata[askforms.EnvelopeMetadataKey].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := raw["submission_id"].(string)
	return id
}

func (c *Client) sendAskSubmissionError(channel, id, message string, errors []askforms.AnswerError) {
	refused, err := json.Marshal(ServerMessage{Type: "chat_event", Channel: channel, Payload: ChatEvent{
		Type: "ask_submission_error", Content: message, Metadata: map[string]any{"submission_id": id, "ask_errors": errors},
	}})
	if err == nil {
		c.safeSend(refused)
	}
}

// NotifyAskSubmissionRejected returns an unpersisted deferred-send refusal to
// the sender's currently connected sockets. It does not enter session replay
// or fan out to HTTP observers or other users. The write pump still checks
// delivery authority on the actual session channel.
func (h *Hub) NotifyAskSubmissionRejected(userID, chatID string, refused *askforms.SubmissionError) {
	if h == nil || userID == "" || chatID == "" || refused == nil {
		return
	}
	channel := "session:" + chatID
	data, ok := h.marshalFrame(ServerMessage{Type: "chat_event", Channel: channel, Payload: ChatEvent{
		Type: "ask_submission_error", Content: refused.Error(), Metadata: map[string]any{"submission_id": refused.SubmissionID, "ask_errors": refused.Errors},
	}})
	if !ok {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for client := range h.clients {
		if client.userID == userID && !client.trySend(data) {
			h.recordDrop(channel, userID)
		}
	}
}
