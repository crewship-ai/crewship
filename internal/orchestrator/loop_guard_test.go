package orchestrator

import "testing"

// A run that never repeats a call the required number of times must not trip,
// even when the same tool is used repeatedly with *different* inputs.
func TestLoopGuard_VariedCallsNeverTrip(t *testing.T) {
	g := &loopGuard{}
	for i := 0; i < 50; i++ {
		if g.observe("Read", map[string]any{"path": i}) {
			t.Fatalf("guard tripped on varied inputs at i=%d", i)
		}
	}
	if g.Tripped() {
		t.Fatal("guard reports tripped after only varied calls")
	}
}

// The identical (tool, input) pair repeated loopGuardThreshold times in a row
// trips exactly once, on the threshold-th call.
func TestLoopGuard_IdenticalCallsTripOnce(t *testing.T) {
	g := &loopGuard{}
	input := map[string]any{"cmd": "status", "flag": true}
	trips := 0
	for i := 1; i <= loopGuardThreshold+3; i++ {
		if g.observe("Bash", input) {
			trips++
			if i != loopGuardThreshold {
				t.Fatalf("tripped on call %d, want first trip on call %d", i, loopGuardThreshold)
			}
		}
	}
	if trips != 1 {
		t.Fatalf("guard tripped %d times, want exactly 1", trips)
	}
	if !g.Tripped() {
		t.Fatal("Tripped() false after threshold reached")
	}
}

// A differing call in the middle of a streak resets it: a genuine poll that
// eventually changes state (or interleaves other work) must survive.
func TestLoopGuard_DifferingCallResetsStreak(t *testing.T) {
	g := &loopGuard{}
	same := map[string]any{"cmd": "poll"}
	// One short of the threshold with identical calls...
	for i := 0; i < loopGuardThreshold-1; i++ {
		if g.observe("Bash", same) {
			t.Fatal("tripped before threshold")
		}
	}
	// ...then a different call breaks the streak.
	if g.observe("Bash", map[string]any{"cmd": "done"}) {
		t.Fatal("tripped on the differing call")
	}
	// Back to the original: streak restarts from 1, must not trip yet.
	if g.observe("Bash", same) {
		t.Fatal("tripped immediately after reset — streak not cleared")
	}
	if g.Tripped() {
		t.Fatal("guard tripped despite a reset breaking the streak")
	}
}

// Identical tool name with structurally-equal input must share a signature
// regardless of Go map ordering, and differing input must not.
func TestToolCallSignature(t *testing.T) {
	a := toolCallSignature("Read", map[string]any{"path": "/x", "n": 1})
	b := toolCallSignature("Read", map[string]any{"n": 1, "path": "/x"})
	if a != b {
		t.Fatalf("signatures differ for equal input:\n a=%q\n b=%q", a, b)
	}
	if toolCallSignature("Read", map[string]any{"path": "/x"}) == toolCallSignature("Read", map[string]any{"path": "/y"}) {
		t.Fatal("signatures collide for different input")
	}
	if toolCallSignature("Read", nil) == toolCallSignature("Write", nil) {
		t.Fatal("signatures collide across tool names")
	}
}
