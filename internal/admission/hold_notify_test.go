package admission

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// A hold whose binding reason never changes must keep saying so. Reporting
// once and then going quiet for up to thirty minutes is the same silence the
// notification was added to remove: after the first line the caller has no way
// to tell a start that is still queued from one that has hung.
func TestAdmit_HoldKeepsReportingWhileTheReasonDoesNotChange(t *testing.T) {
	avail := int64(500)
	c := testController(t, Limits{RequiredFreeMB: 4096}, memReader(&avail, nil))
	// Compress the escalating schedule so the test costs milliseconds. The
	// SHAPE is what is under test — repeat, then back off — not the constants.
	c.holdNotify = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	var at []time.Duration
	start := time.Now()

	_, _ = c.Admit(ctx, "crew-a", "alpha", func(reason, detail string) {
		mu.Lock()
		at = append(at, time.Since(start))
		mu.Unlock()
		if reason != ReasonHostMemory {
			t.Errorf("reason = %q, want %q", reason, ReasonHostMemory)
		}
		if detail == "" {
			t.Error("a repeat notice with no detail tells the caller nothing")
		}
	})

	mu.Lock()
	defer mu.Unlock()
	if len(at) < 2 {
		t.Fatalf("the gate reported the hold %d time(s) over 300ms; after the first line the wait is silent again", len(at))
	}
	if at[0] > 50*time.Millisecond {
		t.Errorf("first notice arrived after %v; the hold must be reported as soon as it starts", at[0])
	}
}

// …and it must not become a per-poll line. The gate polls every 2ms here; a
// notice per poll would be 150 lines in 300ms and would drown the stream it
// was meant to make readable.
func TestAdmit_HoldNoticesAreNotEmittedPerPoll(t *testing.T) {
	avail := int64(500)
	c := testController(t, Limits{RequiredFreeMB: 4096}, memReader(&avail, nil))
	c.holdNotify = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	n := 0
	_, _ = c.Admit(ctx, "crew-a", "alpha", func(string, string) {
		mu.Lock()
		n++
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	// 300ms against a 50ms-then-100ms schedule is at most ~5 notices; the poll
	// count over the same window is ~150.
	if n > 8 {
		t.Fatalf("the gate emitted %d hold notices in 300ms with a 2ms poll — it is reporting per poll", n)
	}
	if n == 0 {
		t.Fatal("the gate emitted no hold notice at all")
	}
}

// A reason that flaps between two legs must not defeat the rate limit. The
// change is worth reporting, but not more often than a person can read.
func TestAdmit_FlappingReasonIsStillRateLimited(t *testing.T) {
	// Memory and concurrency alternate: a second waiter holds the only
	// concurrency slot while the memory reading is toggled underneath.
	avail := int64(500)
	c := New(func(context.Context) Limits {
		return Limits{RequiredFreeMB: 4096, MaxConcurrentStarts: 1}
	}, func() (HostMemory, error) {
		// Alternate on every read: memory short, then fine, then short…
		flapMu.Lock()
		defer flapMu.Unlock()
		flapN++
		if flapN%2 == 0 {
			return HostMemory{AvailableMB: 99999, TotalMB: 128000, SomeStallPct: PressureUnknown}, nil
		}
		return HostMemory{AvailableMB: avail, TotalMB: 128000, SomeStallPct: PressureUnknown}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.pollInterval = time.Millisecond
	c.hostCacheTTL = 0
	c.limitsCacheTTL = time.Millisecond
	c.holdNotify = []time.Duration{time.Second}

	// Occupy the single concurrency slot so the "memory is fine" polls are
	// held by concurrency instead, making the reason alternate.
	blocker, err := c.Admit(context.Background(), "blocker", "b", nil)
	if err != nil {
		t.Fatalf("seeding the concurrency slot: %v", err)
	}
	defer blocker()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var mu sync.Mutex
	n := 0
	_, _ = c.Admit(ctx, "crew-a", "alpha", func(string, string) {
		mu.Lock()
		n++
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	// ~250 polls, each flipping the reason. Without a floor this is ~250
	// notices; with one it is bounded by 250ms / holdNotifyFloor.
	if want := int(250*time.Millisecond/holdNotifyFloor) + 2; n > want {
		t.Fatalf("a flapping reason produced %d notices in 250ms (ceiling %d) — a reason change bypasses the rate limit", n, want)
	}
}

var (
	flapMu sync.Mutex
	flapN  int
)

// The cadence itself, in wall-clock terms, because docs/cli/capacity.mdx
// promises these exact times to users and a 30-minute run budget is what they
// are measured against. Driving notifyDue rather than reading the table back:
// an off-by-one in which entry applies after the n-th notice is invisible to a
// test that just re-adds the constants.
func TestHoldNotifySchedule_LandsWhereTheDocsSayItDoes(t *testing.T) {
	c := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Walk 30 minutes of a hold whose reason never changes, one simulated
	// second at a time, and record when a notice would go out.
	var (
		at         []time.Duration
		notices    int
		notifiedAt time.Time
		now        = time.Now()
	)
	for elapsed := time.Duration(0); elapsed <= 30*time.Minute; elapsed += time.Second {
		if !c.notifyDue(shift(now, notifiedAt, elapsed), notices, false) {
			continue
		}
		at = append(at, elapsed)
		notifiedAt = now.Add(elapsed)
		notices++
	}

	want := []time.Duration{
		0,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		9 * time.Minute,
		19 * time.Minute,
		29 * time.Minute,
	}
	if len(at) != len(want) {
		t.Fatalf("a 30-minute hold produces %d notices at %v, want %d at %v", len(at), at, len(want), want)
	}
	for i := range want {
		if at[i] != want[i] {
			t.Errorf("notice %d lands at %v, want %v (full schedule %v)", i+1, at[i], want[i], at)
		}
	}
}

// shift turns "the last notice went out at simulated time notifiedAt, and it
// is now simulated time now+elapsed" into the wall-clock instant notifyDue
// needs, so the schedule can be walked without a fake clock.
func shift(base, notifiedAt time.Time, elapsed time.Duration) time.Time {
	if notifiedAt.IsZero() {
		return time.Time{}
	}
	return time.Now().Add(-(base.Add(elapsed).Sub(notifiedAt)))
}
