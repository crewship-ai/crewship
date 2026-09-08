package ws

import (
	"context"
	"errors"
	"testing"
)

func TestBroadcastChannelContextStopsWithFullQueue(t *testing.T) {
	h := &Hub{broadcast: make(chan ChannelMessage, 1)}
	if err := h.BroadcastChannelContext(context.Background(), "user", "u1", "conversation.updated", map[string]string{"conversation_id": "c1"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.BroadcastChannelContext(ctx, "user", "u1", "conversation.updated", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("full queue must honor shutdown: %v", err)
	}
	message := <-h.broadcast
	if message.Channel != "user:u1" {
		t.Fatalf("wrong audience: %s", message.Channel)
	}
}
