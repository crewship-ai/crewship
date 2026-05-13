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
	// Join hub.Run before the test returns so the runtime doesn't leak the
	// goroutine into adjacent tests (which would race on shared logger fields).
	hubDone := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(hubDone)
	}()
	defer func() {
		cancel()
		select {
		case <-hubDone:
		case <-time.After(time.Second):
			t.Errorf("hub.Run did not exit within 1s of cancel")
		}
	}()

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

	// Poll for the drop counter to advance — avoids relying on a fixed
	// time.Sleep that flakes under slow CI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.DroppedFrames() > 0 && hub.loggedDropMark.Load() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected drops + loggedDropMark > 0 within deadline; got dropped=%d mark=%d",
		hub.DroppedFrames(), hub.loggedDropMark.Load())
}
