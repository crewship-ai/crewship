//go:build linux

package mooring

import "testing"

func TestOwnerNativeClockHasStableBootIdentityAndMonotonicTime(t *testing.T) {
	clock, e := NewBootOwnerClock()
	if e != nil {
		t.Fatal(e)
	}
	boot, a, e := clock.Now()
	if e != nil || boot == "" || a < 0 {
		t.Fatalf("initial clock: %q %v %v", boot, a, e)
	}
	next, b, e := clock.Now()
	if e != nil || boot != next || b < a {
		t.Fatalf("clock changed identity or reversed: %v", e)
	}
}
