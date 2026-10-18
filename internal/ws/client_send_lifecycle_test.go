package ws

import (
	"sync"
	"testing"
	"time"
)

// Revocation can unregister a connection while async handlers and revoke
// notifications still hold the client. Recovering a send-on-closed panic does
// not synchronize the channel's send and close operations.
func TestClientSendConcurrentUnregister(t *testing.T) {
	hub := newRunningHub(t)
	for range 32 {
		c := newClient(t, hub, "sender")
		for range cap(c.send) {
			c.send <- []byte("full")
		}
		start := make(chan struct{})
		var workers sync.WaitGroup
		for _, send := range []func([]byte) bool{c.safeSend, c.trySend} {
			workers.Go(func() {
				<-start
				for range 128 {
					send([]byte("queued"))
				}
			})
		}
		close(start)
		hub.unregister <- c
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("unregister left a sender blocked")
		}
		waitFor(t, func() bool { return hub.ConnectionCount() == 0 }, "unregister")
		if c.safeSend(nil) || c.trySend(nil) {
			t.Fatal("unregistered client accepted a frame")
		}
		// Repeated teardown must neither close twice nor block another client.
		hub.unregister <- c
	}
}
