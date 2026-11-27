package chatbridge

import (
	"context"
	"errors"

	"github.com/crewship-ai/crewship/internal/conversation"
)

// ResendOfMetadataKey names, in a send's metadata, the saved not-run message that "Send again"
// replays. The content of the send is ignored; the saved message runs.
const ResendOfMetadataKey = "resend_of"

// ErrResendUnavailable refuses "Send again": the message is not this person's, was never saved
// as not run, or already has an answer.
var ErrResendUnavailable = errors.New("message cannot be sent again")

const (
	notRunAgentBusyReason     = "Your message was saved but did not run: the agent is busy with another run. Send it again once the agent is free."
	notRunBusyChatReason      = "Your message was saved but did not run: the agent was still answering in this chat. Send it again once it has finished."
	notRunPendingReviewReason = "Your message was saved but did not run: the agent's hire is awaiting operator approval. Send it again once it is approved."
	notRunRuntimeReason       = "Your message was saved but did not run: the agent's environment could not start. Send it again."
	notRunProvisionReason     = "Your message was saved but did not run: the agent's environment could not be built. Send it again."
)

// resendNotRun finds the message a "Send again" names. Only the author's own message in a
// private chat that was recorded as not run and has no later agent answer qualifies.
func (b *Bridge) resendNotRun(ctx context.Context, info *ChatInfo, userID, chatID string, metadata map[string]any) (conversation.Message, bool, error) {
	raw, present := metadata[ResendOfMetadataKey]
	if !present {
		return conversation.Message{}, false, nil
	}
	id, _ := raw.(string)
	if id == "" || b.convStore == nil || info.Visibility == "group" {
		return conversation.Message{}, false, ErrResendUnavailable
	}
	msgs, err := b.convStore.ReadTranscript(ctx, chatID)
	if err != nil {
		return conversation.Message{}, false, ErrResendUnavailable
	}
	at := -1
	for i, m := range msgs {
		if m.ID == id && m.Role == conversation.RoleUser && m.AuthorUserID == userID {
			at = i
			break
		}
	}
	if at < 0 {
		return conversation.Message{}, false, ErrResendUnavailable
	}
	notRun := false
	for _, m := range msgs[at+1:] {
		if m.Role == conversation.RoleAssistant {
			return conversation.Message{}, false, ErrResendUnavailable
		}
		if m.ID == "notrun-"+id {
			notRun = true
		}
	}
	if !notRun {
		return conversation.Message{}, false, ErrResendUnavailable
	}
	return msgs[at], true, nil
}
