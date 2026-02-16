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
	hub := NewHub(logger)

	if hub.ConnectionCount() != 0 {
		t.Errorf("expected 0 connections, got %d", hub.ConnectionCount())
	}
}

func TestHubRunAndStop(t *testing.T) {
	logger := logging.New("error", "json", nil)
	hub := NewHub(logger)

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
	hub := NewHub(logger)

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
