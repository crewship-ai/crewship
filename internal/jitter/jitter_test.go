package jitter

import (
	"context"
	"testing"
	"time"
)

func TestOffsetIsWithinThePeriod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		period time.Duration
		pick   int64
		want   time.Duration
	}{
		{"zero period", 0, 0, 0},
		{"negative period", -time.Second, 0, 0},
		{"lowest offset", 30 * time.Second, 0, 0},
		{"highest offset", 30 * time.Second, int64(30*time.Second) - 1, 30*time.Second - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := randInt64N
			t.Cleanup(func() { randInt64N = old })
			randInt64N = func(n int64) int64 {
				if n != int64(tc.period) {
					t.Fatalf("drawn from [0, %d), want [0, %d)", n, int64(tc.period))
				}
				return tc.pick
			}
			if got := Offset(tc.period); got != tc.want {
				t.Fatalf("Offset = %v, want %v", got, tc.want)
			}
		})
	}
	// The real source stays inside the period.
	for i := 0; i < 1000; i++ {
		if d := Offset(time.Second); d < 0 || d >= time.Second {
			t.Fatalf("Offset(1s) = %v, outside [0, 1s)", d)
		}
	}
}

func TestStartupWaitsTheOffsetAndHonoursCancellation(t *testing.T) {
	old := randInt64N
	t.Cleanup(func() { randInt64N = old })
	randInt64N = func(int64) int64 { return int64(20 * time.Millisecond) }

	start := time.Now()
	if !Startup(context.Background(), time.Hour) {
		t.Fatal("Startup returned false on a live context")
	}
	if waited := time.Since(start); waited < 20*time.Millisecond {
		t.Fatalf("waited %v, want the 20ms offset", waited)
	}

	randInt64N = func(int64) int64 { return int64(time.Hour) - 1 }
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if Startup(ctx, time.Hour) {
		t.Fatal("Startup returned true although the context was cancelled during the wait")
	}

	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	if Startup(done, 0) {
		t.Fatal("Startup with no wait on a cancelled context must not start the loop")
	}

	stop := make(chan struct{})
	close(stop)
	if StartupStop(stop, time.Hour) {
		t.Fatal("StartupStop returned true on a closed stop channel")
	}
}
