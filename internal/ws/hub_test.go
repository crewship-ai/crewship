package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestHubConnectionCount(t *testing.T) {
	logger := logging.New("error", "json", nil)
	hub := NewHub(logger, nil, NopValidatorForTests, NopSessionsForTests)

	if hub.ConnectionCount() != 0 {
		t.Errorf("expected 0 connections, got %d", hub.ConnectionCount())
	}
}

func TestHubRunAndStop(t *testing.T) {
	logger := logging.New("error", "json", nil)
	hub := NewHub(logger, nil, NopValidatorForTests, NopSessionsForTests)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		hub.Run(ctx)
		close(done)
	}()

	// Give hub time to start
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("hub did not stop in time")
	}
}

func TestServerMessageMarshal(t *testing.T) {
	msg := ServerMessage{
		Type:    "agent_event",
		Channel: "agent:uuid",
		Payload: map[string]string{"event": "text", "content": "hello"},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed["type"] != "agent_event" {
		t.Errorf("expected type agent_event, got %v", parsed["type"])
	}
	if parsed["channel"] != "agent:uuid" {
		t.Errorf("expected channel agent:uuid, got %v", parsed["channel"])
	}
}

func TestBroadcast(t *testing.T) {
	logger := logging.New("error", "json", nil)
	hub := NewHub(logger, nil, NopValidatorForTests, NopSessionsForTests)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	// Broadcast to empty channel should not panic
	hub.Broadcast("test-channel", ServerMessage{
		Type:    "test",
		Payload: "data",
	})

	time.Sleep(10 * time.Millisecond)
}

// TestBroadcastDropsOnFullSendBuffer verifies that a slow consumer (full send
// channel) causes the hub to record dropped frames instead of stalling the
// broadcast loop. Regression guard for the silent-drop bug — before this fix,
// the default arm of the select returned with no side effect and ops had no
// signal that a client was missing events.
func TestBroadcastDropsOnFullSendBuffer(t *testing.T) {
	logger := logging.New("error", "json", nil)
	hub := NewHub(logger, nil, NopValidatorForTests, NopSessionsForTests)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)
	time.Sleep(10 * time.Millisecond)

	clientCtx, clientCancel := context.WithCancel(context.Background())
	stuck := &Client{
		hub:      hub,
		userID:   "stuck-user",
		channels: map[string]bool{"test-channel": true},
		// Tiny buffer that we never drain, so the second send must drop.
		send:   make(chan []byte, 1),
		ctx:    clientCtx,
		cancel: clientCancel,
	}
	defer clientCancel() // tear down even on early failure to avoid goroutine leak.
	hub.mu.Lock()
	if hub.channels["test-channel"] == nil {
		hub.channels["test-channel"] = make(map[*Client]bool)
	}
	hub.channels["test-channel"][stuck] = true
	hub.mu.Unlock()

	// Send enough to cross dropLogThreshold so the dedup log fires at least once.
	for i := 0; i < dropLogThreshold*2+1; i++ {
		hub.Broadcast("test-channel", ServerMessage{Type: "test", Payload: i})
	}
	time.Sleep(50 * time.Millisecond)

	if got := hub.DroppedFrames(); got == 0 {
		t.Fatalf("expected drops on stuck consumer, got 0")
	}
	// Verify the threshold-based dedup actually advanced — without this assert
	// the test would still pass even if recordDrop never logged anything.
	if mark := hub.loggedDropMark.Load(); mark == 0 {
		t.Fatalf("expected loggedDropMark > 0 after %d drops, got 0", hub.DroppedFrames())
	}
}
