package ws

import (
	"encoding/json"
	"testing"
	"time"
)

// recvFrame drains one frame off a client's send buffer.
func recvFrame(t *testing.T, c *Client) ServerMessage {
	t.Helper()
	select {
	case raw := <-c.send:
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal %q: %v", raw, err)
		}
		return msg
	case <-time.After(time.Second):
		t.Fatal("no frame received")
		return ServerMessage{}
	}
}

// An error frame is only actionable if the receiver can tell WHICH conversation
// it is about. A refusal of a send names the chat it refused — derived from the
// payload, because a client is not obliged to set Channel on a send_message —
// so a chat surface can attribute it to the right transcript instead of showing
// it in whatever chat happens to be open (hooks/use-chat.ts drops unaddressed
// error frames for exactly that reason).
func TestHandleSendMessage_RefusalNamesTheSessionChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authorizer  ChannelAuthorizer
		chatHandler ChatHandler
		payload     sendMessagePayload
		wantChannel string
		wantReason  string
	}{
		{
			name:        "denied session",
			authorizer:  denyAllAuthorizer{},
			chatHandler: &stubChatHandler{},
			payload:     sendMessagePayload{ChatID: "s1", Content: "hi"},
			wantChannel: "session:s1",
			wantReason:  "access denied",
		},
		{
			name:        "no chat handler wired",
			authorizer:  allowAllAuthorizer{},
			chatHandler: nil,
			payload:     sendMessagePayload{ChatID: "s1", Content: "hi"},
			wantChannel: "session:s1",
			wantReason:  "chat not available",
		},
		{
			name:        "missing content",
			authorizer:  allowAllAuthorizer{},
			chatHandler: &stubChatHandler{},
			payload:     sendMessagePayload{ChatID: "s1"},
			wantChannel: "session:s1",
			wantReason:  "session_id and content required",
		},
		{
			name:        "missing session id — nothing to address it to",
			authorizer:  allowAllAuthorizer{},
			chatHandler: &stubChatHandler{},
			payload:     sendMessagePayload{Content: "hi"},
			wantChannel: "",
			wantReason:  "session_id and content required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hub := newRunningHub(t)
			hub.SetChannelAuthorizer(tc.authorizer)
			if tc.chatHandler != nil {
				hub.SetChatHandler(tc.chatHandler)
			}
			c := newClient(t, hub, "u1")

			body, _ := json.Marshal(tc.payload)
			// Channel deliberately empty: the frame's own payload is the only
			// thing that says which chat this send is about.
			c.handleSendMessage(ClientMessage{Type: "send_message", Payload: body})

			msg := recvFrame(t, c)
			if msg.Type != "error" {
				t.Fatalf("type = %q, want error frame", msg.Type)
			}
			if msg.Channel != tc.wantChannel {
				t.Errorf("channel = %q, want %q — an error frame the client cannot attribute "+
					"is either lost or shown against the wrong chat", msg.Channel, tc.wantChannel)
			}
			assertReasonPayload(t, msg, tc.wantReason)
		})
	}
}
