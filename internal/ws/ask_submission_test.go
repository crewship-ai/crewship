package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type askFailureHandler struct{ accepted, busy bool }

func (h askFailureHandler) HandleChatMessage(_ context.Context, user, _, _ string, emit func(ChatEvent), _ ...ChatMessageOption) error {
	if h.busy {
		return ErrAgentBusy
	}
	if h.accepted {
		emit(ChatEvent{Type: "user_message", Content: "Answer: approved", Metadata: map[string]any{"author_user_id": user, "ask_submission_id": "sub-correlated"}})
	}
	emit(ChatEvent{Type: "error", Content: "runtime unavailable"})
	return errors.New("synthetic handler failure")
}

// A private refusal is proof about this submission. A shared runtime failure
// after its append is not a reason to undo a successfully accepted form.
func TestAskSubmissionFailuresAreCorrelatedAndPrivateUntilAccepted(t *testing.T) {
	for _, tc := range []struct {
		name           string
		accepted, busy bool
	}{
		{"before append", false, false}, {"busy before append", false, true}, {"after append", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := newRunningHub(t)
			hub.SetChannelAuthorizer(allowAllAuthorizer{})
			hub.SetChatHandler(askFailureHandler{accepted: tc.accepted, busy: tc.busy})
			sender, other := newClient(t, hub, "ask-author"), newClient(t, hub, "other-author")
			sender.subscribe("session:s1")
			other.subscribe("session:s1")
			body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "Answer: approved", Metadata: map[string]any{"ask_submission": map[string]any{"submission_id": "sub-correlated"}}})
			sender.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})
			deadline := time.After(2 * time.Second)
			sawReceipt, sawError, finished := false, false, false
			for !finished {
				select {
				case raw := <-sender.send:
					var message ServerMessage
					if err := json.Unmarshal(raw, &message); err != nil {
						t.Fatal(err)
					}
					if message.Type != "chat_event" {
						continue
					}
					event := message.Payload.(map[string]any)
					switch event["type"] {
					case "ask_submission_error":
						if tc.accepted {
							t.Fatalf("postappend failure rejected an already accepted ask: %s", raw)
						}
						if message.Seq != 0 {
							t.Fatalf("refusal entered replay: %s", raw)
						}
						metadata := event["metadata"].(map[string]any)
						if metadata["submission_id"] != "sub-correlated" {
							t.Fatalf("uncorrelated refusal: %s", raw)
						}
						finished = true
					case "user_message":
						sawReceipt = true
					case "error":
						if !tc.accepted {
							t.Fatalf("preappend failure leaked into shared run: %s", raw)
						}
						sawError = true
					case "done":
						if !tc.accepted {
							t.Fatalf("preappend refusal emitted terminal run event: %s", raw)
						}
						finished = true
					}
				case <-deadline:
					t.Fatal("submission never settled")
				}
			}
			if tc.accepted {
				if !sawReceipt || !sawError {
					t.Fatalf("postappend stream lost receipt/runtime error: receipt=%v error=%v", sawReceipt, sawError)
				}
				return
			}
			// This send emitted no run events at all: another subscriber's active run
			// and replay must be untouched by the rejected form.
			select {
			case raw := <-other.send:
				t.Fatalf("private refusal reached another subscriber: %s", raw)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}
