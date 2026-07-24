package ws

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// TestJournalChannel_OrderedSingleDeliveryToSubscriber pins the real-hub half
// of the journal→WS bridge contract (internal/server/journal_ws_bridge.go): the
// bridge's drain goroutine calls BroadcastChannel("journal", ws, "journal.entry",
// …) once per feed-relevant entry, in commit order. This test drives that same
// call sequence through a REAL running Hub (not the bridge's fake broadcaster)
// and asserts a client subscribed to journal:{ws} receives every frame
//
//   - in the order broadcast (FIFO through the 256-deep broadcast channel and
//     the dispatch loop), and
//   - exactly once (no double-delivery on a single subscription).
func TestJournalChannel_OrderedSingleDeliveryToSubscriber(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("journal:ws1")

	const n = 25
	for i := 0; i < n; i++ {
		// Mirror the bridge's payload shape: a serialized journal entry map
		// carrying at least an id. BroadcastChannel wraps it in a
		// ServerMessage{Type:"journal.entry", Channel:"journal:ws1"}.
		hub.BroadcastChannel("journal", "ws1", "journal.entry", map[string]any{"id": strconv.Itoa(i)})
	}

	seen := make(map[string]int, n)
	for i := 0; i < n; i++ {
		raw := recvOrTimeout(t, c.send)
		var msg ServerMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("frame %d: unmarshal: %v", i, err)
		}
		if msg.Type != "journal.entry" {
			t.Fatalf("frame %d: type = %q, want journal.entry", i, msg.Type)
		}
		if msg.Channel != "journal:ws1" {
			t.Fatalf("frame %d: channel = %q, want journal:ws1", i, msg.Channel)
		}
		payload, ok := msg.Payload.(map[string]any)
		if !ok {
			t.Fatalf("frame %d: payload type = %T, want map[string]any", i, msg.Payload)
		}
		gotID, _ := payload["id"].(string)
		// Ordering: the i-th frame received must be the i-th broadcast.
		if gotID != strconv.Itoa(i) {
			t.Fatalf("frame %d: id = %q, want %q (out-of-order delivery)", i, gotID, strconv.Itoa(i))
		}
		seen[gotID]++
	}

	// No double-delivery: every id landed exactly once, and nothing extra is
	// queued beyond the n frames we broadcast.
	for id, count := range seen {
		if count != 1 {
			t.Errorf("id %s delivered %d times, want exactly 1", id, count)
		}
	}
	select {
	case extra := <-c.send:
		t.Fatalf("unexpected extra frame after %d delivered: %q", n, string(extra))
	case <-time.After(50 * time.Millisecond):
	}
}

// TestJournalChannel_NotDeliveredToWorkspaceSubscriber is the firehose
// regression guard at the hub level: the bridge deliberately fans out on the
// dedicated opt-in journal:{ws} channel, NOT the workspace:{ws} channel every
// dashboard tab auto-subscribes to. A client subscribed only to workspace:ws1
// must therefore receive nothing when a journal.entry is broadcast on
// journal:ws1 — proving the two channels are isolated and a non-journal tab
// never pays for the journal stream.
func TestJournalChannel_NotDeliveredToWorkspaceSubscriber(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	journalClient := newClient(t, hub, "u1")
	journalClient.subscribe("journal:ws1")

	wsClient := newClient(t, hub, "u2")
	wsClient.subscribe("workspace:ws1")

	hub.BroadcastChannel("journal", "ws1", "journal.entry", map[string]any{"id": "e1"})

	// The journal subscriber gets it…
	raw := recvOrTimeout(t, journalClient.send)
	var msg ServerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("journal subscriber: unmarshal: %v", err)
	}
	if msg.Channel != "journal:ws1" {
		t.Fatalf("journal subscriber: channel = %q, want journal:ws1", msg.Channel)
	}

	// …the workspace-only subscriber must NOT (no firehose leak).
	select {
	case leaked := <-wsClient.send:
		t.Fatalf("workspace:ws1 subscriber received a journal frame (firehose leak): %q", string(leaked))
	case <-time.After(100 * time.Millisecond):
	}
}
