//go:build !crewship_testbarrier || !unix

package appengine

import "context"

const barrierCompiledIn = false

// ConfigureTestBarrier is a no-op in normal builds, even when the barrier
// environment is set: the hook is not compiled in.
func ConfigureTestBarrier(int) (bool, error) { return false, nil }

// TestBarrierActive is always false in normal builds.
func TestBarrierActive() bool { return false }

func testBarrier(context.Context, barrierPoint, Key, string, string) error { return nil }
