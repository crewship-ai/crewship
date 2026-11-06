package api

import (
	"context"
	"time"
)

// collaborationGateWatchInterval bounds how long a lapsed gate waits for its
// reopen. Each tick is one indexed read per connection, never a write.
const collaborationGateWatchInterval = time.Second

// The child anchors a read lease at the renewal REQUEST; the SQL gate is anchored
// at the host's REPLY. A renewal delayed past the lease therefore renews the row
// while the child has closed that recipient for good: it stops asking, and holds
// Saved frames for a new gate of the same participant. Rotation had three
// triggers, a client frame, a renewal and an output, and a quiet recipient has
// none of them left, so its frames waited for the next thing it happened to send.
//
// This is only a fourth trigger. closeObsoleteGate still proves the expiry in the
// writer transaction, and the reopen still requires the original admission, so a
// lapsed gate never grants anything by being noticed here.
func (c *collaborationHTTPConnection) gateLapsed(ctx context.Context) (bool, error) {
	c.mu.Lock()
	gate := c.joined.gate
	live := !c.closed && c.joinedDelivered && c.participantBinding != nil && c.joined.delivery != nil && c.transition == nil
	c.mu.Unlock()
	if !live {
		return false, nil
	}
	d := c.document
	var boot string
	var expires int64
	var closed bool
	err := d.manager.pages.db.QueryRowContext(ctx, `SELECT boot_id,expires_ns,closed FROM mooring_delivery_gates WHERE id=? AND page_id=? AND workspace_id=? AND application_id=?`, gate.GateID, d.key.ArtifactID, d.key.WorkspaceID, d.key.ApplicationID).Scan(&boot, &expires, &closed)
	if err != nil {
		return false, err
	}
	currentBoot, now, err := d.manager.clock.Now()
	if err != nil {
		return false, err
	}
	// A closed row has its own path: revocation must not be rerouted through here.
	return !closed && boot == currentBoot && now >= 0 && int64(now) >= expires, nil
}

// Does not wait for Joined: that continuation already owns the catch-up, and the
// watch must stay stoppable. A failure ends the document exactly as a failed
// rotation from any other trigger does, unless this socket closed meanwhile.
func (c *collaborationHTTPConnection) reopenLapsedGate(ctx context.Context) {
	c.deliveryMu.Lock()
	_, err := c.startGateTransition(ctx)
	c.deliveryMu.Unlock()
	if err == nil {
		return
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		c.document.cancel()
	}
}

// startGateWatch runs for one socket. Stop it before closing the connection and
// before cancelling ctx: a rotation cut off by its own socket's teardown must not
// read as a failure and end the document for everyone else.
func (c *collaborationHTTPConnection) startGateWatch(ctx context.Context, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A failed read proves nothing about the gate; the next tick retries.
				if lapsed, err := c.gateLapsed(ctx); err == nil && lapsed {
					c.reopenLapsedGate(ctx)
				}
			}
		}
	}()
	return func() { close(stop); <-done }
}
