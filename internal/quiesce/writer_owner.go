package quiesce

import (
	"errors"
	"fmt"
)

var ErrWriterOwnerLost = errors.New("quiesce: exclusive database writer ownership is unavailable")

// SetWriterOwner installs the server's process-lifetime ownership verifier.
// It cannot be replaced during a window. Embedded callers without a server
// owner keep their existing contract; production startup always binds one.
func (c *Controller) SetWriterOwner(check func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window != nil {
		return ErrAlreadyHeld
	}
	c.writerOwner = check
	return nil
}

func verifyWriterOwner(check func() error) error {
	if check == nil {
		return nil
	}
	if err := check(); err != nil {
		return fmt.Errorf("%w: %w", ErrWriterOwnerLost, err)
	}
	return nil
}

// VerifyWriterOwner checks the exact owner captured by Begin, never a newly
// installed owner after a lost epoch. Call it before and after the copy.
func (w *Window) VerifyWriterOwner() error { return verifyWriterOwner(w.writerOwner) }

// VerifyWriterOwner checks this controller's currently configured server owner.
func (c *Controller) VerifyWriterOwner() error {
	c.mu.Lock()
	check := c.writerOwner
	c.mu.Unlock()
	return verifyWriterOwner(check)
}
