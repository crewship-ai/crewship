package server

import (
	"context"
	"time"
)

// sheetSlotWait is how long an agent's Sheet change waits for a free engine
// session before it is answered "busy": long enough for another agent's batch
// to finish, well inside a chat run's engine window (the lease is renewed while
// it waits).
const sheetSlotWait = 45 * time.Second

// waitForSlot calls try until it is not refused as busy, for at most wait.
// Nothing runs while it waits, so a refusal after the wait (or once ctx ends)
// is still the plain busy answer and the agent's request stays retryable.
func waitForSlot(ctx context.Context, wait, poll time.Duration, busy func(error) bool, try func() error) error {
	// Leave the operation itself (open, apply, save) most of what remains.
	if dl, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(dl)/3)
	}
	deadline := time.Now().Add(wait)
	for {
		err := try()
		if !busy(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(min(poll, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}
