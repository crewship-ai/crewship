// Package jitter spreads the first run of periodic background loops.
//
// Every ticker-driven sweeper starts at process boot with a fixed period, so a
// deploy re-synchronises all their phases and the 30 s sweepers land on the
// same second, contending for SQLite's single writer together (#1891). A
// random offset before each loop's first tick keeps their phases apart for the
// life of the process. Daily jobs scheduled by wall-clock hour are staggered
// on purpose and do not use this.
package jitter

import (
	"context"
	"math/rand/v2"
	"time"
)

// randInt64N is swapped by tests for a deterministic offset.
var randInt64N = rand.Int64N

// Offset returns a uniformly random delay in [0, period). A non-positive
// period yields zero.
func Offset(period time.Duration) time.Duration {
	if period <= 0 {
		return 0
	}
	return time.Duration(randInt64N(int64(period)))
}

// Startup waits a random offset in [0, period) before a periodic loop begins.
// It returns false if ctx is done first, in which case the loop should not
// start; true otherwise. A non-positive period does not wait.
func Startup(ctx context.Context, period time.Duration) bool {
	return Wait(ctx, nil, period)
}

// StartupStop is Startup for loops that stop on a channel instead of a
// context. It returns false if stop closes first.
func StartupStop(stop <-chan struct{}, period time.Duration) bool {
	return Wait(context.Background(), stop, period)
}

// Wait is Startup for loops that stop on either a context or a channel; a nil
// stop channel is never ready.
func Wait(ctx context.Context, stop <-chan struct{}, period time.Duration) bool {
	d := Offset(period)
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-stop:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
