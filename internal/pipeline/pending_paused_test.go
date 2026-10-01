package pipeline

import (
	"context"
	"testing"
)

// A held pending-run dispatcher neither fires nor expires: the sweep returns
// before it touches the store at all (a nil store would panic otherwise).
func TestPendingRunDispatcherSweepIsHeldWhilePaused(t *testing.T) {
	d := NewPendingRunDispatcher(nil, nil, nil)
	d.SetPaused(func() bool { return true })
	d.sweep(context.Background())
}
