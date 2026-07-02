package ws

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// chatEventPayloadType extracts payload.type from a decoded chat_event frame.
func chatEventPayloadType(msg ServerMessage) string {
	pmap, ok := msg.Payload.(map[string]interface{})
	if !ok {
		return ""
	}
	typ, _ := pmap["type"].(string)
	return typ
}

// TestHandleSendMessageAgentBusySenderOnly is the regression test for the
// busy-rejection fan-out bug: when HandleChatMessage rejects a send because
// the chat already has a live run (ws.ErrAgentBusy), the rejection must be
// delivered to the REJECTED SENDER ONLY — a private agent_busy frame on
// their socket, like the same-user cancelKey guard's safeSend reply. On
// broken code the rejection went out through the emit/broadcast path as an
// error + terminal done on the shared session channel, so the WINNING
// user's client saw a bare done, finalized its live streaming turn, and
// unlocked its composer mid-generation.
//
// Asserts, with two clients subscribed to the same session channel:
//   - the sender receives an agent_busy chat_event (not a generic error),
//   - the sender receives NO terminal done for the bounced send,
//   - the other subscriber receives NO chat_event at all (no agent_busy,
//     no error, no done) — nothing it could interpret as a run ending.
func TestHandleSendMessageAgentBusySenderOnly(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	// The bridge wraps the sentinel with chat context; errors.Is must still match.
	handler := &stubChatHandler{err: fmt.Errorf("chat s1: %w", ErrAgentBusy)}
	hub.SetChatHandler(handler)

	sender := newClient(t, hub, "u-rejected")
	other := newClient(t, hub, "u-winner")
	sender.subscribe("session:s1")
	other.subscribe("session:s1")

	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	sender.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

	// Drain the sender until the agent_busy notice arrives. A generic error
	// chat_event or a terminal done on the way is the bug this test pins.
	sawBusy := false
	deadline := time.After(time.Second)
	for !sawBusy {
		select {
		case raw := <-sender.send:
			var msg ServerMessage
			_ = json.Unmarshal(raw, &msg)
			if msg.Type != "chat_event" {
				continue // run_begin etc.
			}
			switch chatEventPayloadType(msg) {
			case "agent_busy":
				sawBusy = true
			case "error":
				t.Fatalf("busy rejection reached the sender as a generic error chat_event: %s", raw)
			case "done":
				t.Fatalf("rejected sender received a terminal done frame: %s", raw)
			}
		case <-deadline:
			t.Fatal("rejected sender never received the agent_busy notice")
		}
	}

	// The sender must not receive a trailing done after the busy notice.
	select {
	case raw := <-sender.send:
		var msg ServerMessage
		_ = json.Unmarshal(raw, &msg)
		if msg.Type == "chat_event" {
			t.Fatalf("unexpected chat_event after the sender-only busy notice: %s", raw)
		}
	case <-time.After(100 * time.Millisecond):
	}

	// The other subscriber must see NO chat_event from the bounced send. Any
	// broadcast the goroutine performed was enqueued before the sender's
	// (later) private busy frame, so by now it would already be in the
	// channel buffer — a bounded drain is deterministic, not timing-lucky.
	for {
		select {
		case raw := <-other.send:
			var msg ServerMessage
			_ = json.Unmarshal(raw, &msg)
			if msg.Type == "chat_event" {
				t.Fatalf("busy rejection leaked a chat_event to another session subscriber: %s", raw)
			}
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}
