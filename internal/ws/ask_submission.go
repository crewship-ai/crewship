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
