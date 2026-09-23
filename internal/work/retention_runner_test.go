package work

import (
	"context"
	"testing"
	"time"
)

func TestStartRetentionSweeper_ExpiresTerminalReceiptButKeepsQueuedDelivery(t *testing.T) {
	s, db, clock := newTestStore(t)
	terminal := acceptDelivery(t, s, db, retDelivery("expired", "endpoint", []byte("terminal")), retWork())
	finish(t, s, terminal.WorkID, StateSucceeded)
	queued := acceptDelivery(t, s, db, retDelivery("pending", "endpoint", []byte("queued")), retWork())
	clock.Advance(31 * 24 * time.Hour)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		StartRetentionSweeper(ctx, s, nil, time.Hour)
	}()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for deliveryExists(t, db, terminal.DeliveryID) {
		select {
		case <-deadline.C:
			t.Fatal("startup retention sweep did not expire the terminal receipt")
		case <-ticker.C:
		}
	}
	if !deliveryExists(t, db, queued.DeliveryID) || string(rawBodyOf(t, db, queued.DeliveryID)) != "queued" {
		t.Fatal("startup retention sweep removed a non-terminal delivery")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention sweeper did not stop with its context")
	}
}
