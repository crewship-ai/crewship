package seeddata

import "testing"

func TestPackForRoutineEmptyDoesNotSelectPackWithoutProbe(t *testing.T) {
	if pack, ok := PackForRoutine(""); ok {
		t.Fatalf("empty routine selected pack %q; absence of a probe must not be a lookup key", pack.Slug)
	}
}
