package admission

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedLimits returns a resolver that always answers lim.
func fixedLimits(lim Limits) LimitsResolver {
	return func(context.Context) Limits { return lim }
}

// memReader returns a HostReader serving whatever the pointer currently holds,
// so a test can free memory mid-flight.
func memReader(avail *int64, stall *float64) HostReader {
	return func() (HostMemory, error) {
		s := PressureUnknown
		if stall != nil {
			s = *stall
		}
		return HostMemory{
			AvailableMB:  atomic.LoadInt64(avail),
			TotalMB:      16384,
			SomeStallPct: s,
		}, nil
	}
}

func testController(t *testing.T, lim Limits, read HostReader) *Controller {
	t.Helper()
	c := New(fixedLimits(lim), read, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Tight poll so a blocked Admit re-checks quickly; the host cache must be
	// shorter still or the second read would serve the first answer forever.
	c.pollInterval = 2 * time.Millisecond
	c.hostCacheTTL = time.Millisecond
	return c
}

// The headline behaviour: with less host memory available than one more agent
// needs, a container start is HELD — not refused, not admitted — and it is
// admitted the moment memory frees. This is the whole feature; the owner's
// "pause the agent, resume when memory frees" one step earlier, before the
// container exists.
func TestAdmit_HoldsUntilHostMemoryFrees(t *testing.T) {
	avail := int64(1000)
	c := testController(t, Limits{RequiredFreeMB: 3072}, memReader(&avail, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	admitted := make(chan struct{})
	go func() {
		release, err := c.Admit(ctx, "crew-a", "alpha", nil)
		if err != nil {
			return
		}
		defer release()
		close(admitted)
	}()

	select {
	case <-admitted:
		t.Fatal("admitted with 1000 MiB available against a 3072 MiB requirement — the host memory gate did not hold")
	case <-time.After(60 * time.Millisecond):
	}

	// Memory frees. No signal, no notification — the gate must notice on its
	// own, because nothing in the kernel is going to tell it.
	atomic.StoreInt64(&avail, 8000)

	select {
	case <-admitted:
	case <-time.After(3 * time.Second):
		t.Fatal("still held after memory freed — the gate never re-reads the host signal")
	}
}

// The reason a held run is held has to be legible, and it has to name the
// numbers, or an operator cannot tell a capacity hold from a hang.
func TestAdmit_HeldRunIsVisibleWithReasonAndCrew(t *testing.T) {
	avail := int64(500)
	c := testController(t, Limits{RequiredFreeMB: 4096}, memReader(&avail, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	onHold := make(chan Hold, 1)
	go func() {
		_, _ = c.Admit(ctx, "crew-a", "alpha", func(reason, detail string) {
			select {
			case onHold <- Hold{CrewID: "crew-a", CrewSlug: "alpha", Reason: reason, Detail: detail}:
			default:
			}
		})
	}()

	var h Hold
	select {
	case h = <-onHold:
	case <-time.After(2 * time.Second):
		t.Fatal("Admit blocked without ever reporting a hold — a silent queue is indistinguishable from a hang")
	}
	if h.CrewID != "crew-a" || h.CrewSlug != "alpha" {
		t.Errorf("hold identifies crew %q/%q, want crew-a/alpha", h.CrewID, h.CrewSlug)
	}
	if h.Reason != ReasonHostMemory {
		t.Errorf("hold reason = %q, want %q", h.Reason, ReasonHostMemory)
	}
	if h.Detail == "" {
		t.Error("hold carries no detail; the message must name the numbers")
	}

	// And the same fact must be queryable, not only pushed once.
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := c.Snapshot(ctx)
		if len(snap.Held) == 1 && snap.Held[0].CrewID == "crew-a" && snap.Held[0].Reason == ReasonHostMemory {
			if snap.HeldTotal == 0 {
				t.Error("HeldTotal = 0 after a hold")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Snapshot never reported the held crew: %+v", snap.Held)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	// Cancelling must retract the hold, or the surface accumulates ghosts.
	deadline = time.Now().Add(2 * time.Second)
	for {
		if len(c.Snapshot(context.Background()).Held) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("held entry survived context cancellation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Concurrent container CREATES are what is unbounded today: runSem sits inside
// RunAgent and every caller has already created its container by then. This
// pins the bound at the create.
func TestAdmit_BoundsConcurrentContainerStarts(t *testing.T) {
	const cap, n = 3, 24
	avail := int64(65536)
	c := testController(t, Limits{MaxConcurrentStarts: cap, RequiredFreeMB: 1024}, memReader(&avail, nil))

	var mu sync.Mutex
	var inFlight, peak int

	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := c.Admit(context.Background(), fmt.Sprintf("crew-%d", i), "s", nil)
			if err != nil {
				t.Errorf("Admit: %v", err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			<-gate
			mu.Lock()
			inFlight--
			mu.Unlock()
			release()
		}(i)
	}

	// Let the first wave pile up against the bound, then release everyone.
	time.Sleep(80 * time.Millisecond)
	close(gate)
	wg.Wait()

	mu.Lock()
	got := peak
	mu.Unlock()
	if got > cap {
		t.Fatalf("peak concurrent container starts = %d, exceeds cap %d — creates are not bounded", got, cap)
	}
	if got == 0 {
		t.Fatal("nothing was ever admitted")
	}
}

// Twenty crews sharing a cron minute must not hit the daemon in the same
// millisecond: netns creation takes a global lock measured at 1.45 ms serial
// but ~418 ms at 24x concurrency.
func TestAdmit_StaggersSimultaneousStarts(t *testing.T) {
	const interval = 25 * time.Millisecond
	avail := int64(65536)
	c := testController(t, Limits{MinStartInterval: interval, RequiredFreeMB: 1024}, memReader(&avail, nil))

	var mu sync.Mutex
	var stamps []time.Time

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := c.Admit(context.Background(), fmt.Sprintf("crew-%d", i), "s", nil)
			if err != nil {
				t.Errorf("Admit: %v", err)
				return
			}
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
			release()
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) != 4 {
		t.Fatalf("got %d admissions, want 4", len(stamps))
	}
	// Total span, not pairwise: the goroutines record their stamp after
	// Admit returns, so scheduling jitter can reorder adjacent pairs while
	// the pacing floor still holds across the whole wave.
	oldest, newest := stamps[0], stamps[0]
	for _, s := range stamps[1:] {
		if s.Before(oldest) {
			oldest = s
		}
		if s.After(newest) {
			newest = s
		}
	}
	if span := newest.Sub(oldest); span < 3*interval {
		t.Fatalf("4 admissions spanned %v, want at least %v — simultaneous wakes are not staggered", span, 3*interval)
	}
}

// PSI is the secondary veto: MemAvailable counts reclaimable page cache, so a
// host whose "available" memory is entirely hot cache looks roomy right up
// until it thrashes. A host already stalling must not be handed another
// container.
func TestAdmit_HighMemoryPressureVetoesDespiteFreeMemory(t *testing.T) {
	avail := int64(65536) // plenty, by MemAvailable
	stall := 55.0         // but the host is stalling on memory more than half the time
	c := testController(t, Limits{RequiredFreeMB: 1024, MaxPressurePct: 20}, memReader(&avail, &stall))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Admit(ctx, "crew-a", "alpha", nil)
	if err == nil {
		t.Fatal("admitted a start on a host stalling at 55% memory pressure; the PSI veto did nothing")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the ctx deadline (i.e. it was held)", err)
	}
}

// A host that publishes no PSI (CONFIG_PSI=n) must not be treated as a host
// under infinite pressure. PressureUnknown is not a reading.
func TestAdmit_UnknownPressureDoesNotVeto(t *testing.T) {
	avail := int64(65536)
	c := testController(t, Limits{RequiredFreeMB: 1024, MaxPressurePct: 20}, memReader(&avail, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := c.Admit(ctx, "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("Admit: %v — an absent PSI reading must not veto", err)
	}
	release()
}

// macOS, stated plainly. Neither /proc/meminfo nor /proc/pressure/memory
// exists there, and the Apple provider runs on it. The memory gate DEGRADES TO
// NOTHING rather than gating on a signal it cannot read — but the parts that
// need no host signal (the concurrency bound, the stagger) still apply.
func TestAdmit_NoHostSignal_DegradesToNoMemoryGate(t *testing.T) {
	read := func() (HostMemory, error) {
		return HostMemory{}, fmt.Errorf("%w: /proc/meminfo: no such file or directory", ErrHostSignalUnavailable)
	}
	c := testController(t, Limits{RequiredFreeMB: 1 << 40, MaxPressurePct: 1}, read)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, err := c.Admit(ctx, "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("Admit: %v — with no readable host signal the memory gate must stand down, not fail closed", err)
	}
	release()

	snap := c.Snapshot(context.Background())
	if snap.HostSignalAvailable {
		t.Error("Snapshot reports the host signal as available on a host that has none")
	}
	if snap.HostSignalError == "" {
		t.Error("Snapshot hides why the host signal is unavailable; an operator must be able to see the gate is inactive")
	}
}

// The concurrency bound must still bite with no host signal — it needs no
// kernel file, and it is the half of admission control that protects the
// daemon rather than the host's RAM.
func TestAdmit_NoHostSignal_StillBoundsConcurrency(t *testing.T) {
	read := func() (HostMemory, error) {
		return HostMemory{}, ErrHostSignalUnavailable
	}
	c := testController(t, Limits{MaxConcurrentStarts: 1, RequiredFreeMB: 4096}, read)

	release, err := c.Admit(context.Background(), "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Admit(ctx, "crew-b", "beta", nil); err == nil {
		t.Fatal("second start admitted while the first held the only slot")
	}
}

// A nil Controller is the "no admission control configured" case (tests,
// headless harnesses, a provider constructed without one). It must be a
// pass-through, not a panic and not a block.
func TestAdmit_NilController_IsPassThrough(t *testing.T) {
	var c *Controller
	release, err := c.Admit(context.Background(), "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("nil controller Admit: %v", err)
	}
	release()
	release() // idempotent
}

// Release must be idempotent: the provider defers it on paths that also return
// an error, and a double decrement would hand out a phantom slot.
func TestRelease_IsIdempotent(t *testing.T) {
	avail := int64(65536)
	c := testController(t, Limits{MaxConcurrentStarts: 1, RequiredFreeMB: 1024}, memReader(&avail, nil))

	release, err := c.Admit(context.Background(), "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	release()
	release()
	release()

	if got := c.Snapshot(context.Background()).InFlightStarts; got != 0 {
		t.Fatalf("InFlightStarts = %d after repeated release, want 0", got)
	}

	// The slot must not have been double-freed into a second slot.
	r1, err := c.Admit(context.Background(), "crew-b", "beta", nil)
	if err != nil {
		t.Fatalf("Admit after release: %v", err)
	}
	defer r1()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Admit(ctx, "crew-c", "gamma", nil); err == nil {
		t.Fatal("a repeated release handed out a phantom concurrency slot")
	}
}

// Zero means "off" for every limit, so an operator can disable any one leg
// without disabling the others.
func TestAdmit_ZeroLimitsDisableEachLeg(t *testing.T) {
	avail := int64(1) // 1 MiB free: would fail any real memory requirement
	stall := 99.0
	c := testController(t, Limits{}, memReader(&avail, &stall))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := c.Admit(ctx, "crew-a", "alpha", nil)
	if err != nil {
		t.Fatalf("Admit with all limits zero: %v", err)
	}
	release()

	// And each leg has to be independently disabled, not merely skipped as a
	// group. With the memory leg ON and the pressure ceiling at 0, a host
	// stalling at 99% must still be admitted — otherwise "0 = off" quietly
	// becomes "0 = veto everything" for whoever turns only one leg on.
	c2 := testController(t, Limits{RequiredFreeMB: 1, MaxPressurePct: 0}, memReader(&avail, &stall))
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	release2, err := c2.Admit(ctx2, "crew-b", "beta", nil)
	if err != nil {
		t.Fatalf("Admit with MaxPressurePct=0 on a 99%%-stalled host: %v", err)
	}
	release2()
}

// The limits resolver reads app_settings — five indexed SELECTs against SQLite.
// A held start re-checks the world several times a second and there can be a
// waiter per crew, so resolving per waiter per poll turns a twenty-crew wake
// into hundreds of queries a second against the same database the runs
// themselves are using. The reading is cached for a short window instead:
// still live enough that `crewship instance settings set` lands on the next
// held start, without the fan-out.
func TestAdmit_LimitsAreNotReResolvedOnEveryPoll(t *testing.T) {
	var resolved atomic.Int64
	avail := int64(500)
	c := New(func(context.Context) Limits {
		resolved.Add(1)
		return Limits{RequiredFreeMB: 4096}
	}, memReader(&avail, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.pollInterval = time.Millisecond
	c.hostCacheTTL = time.Millisecond
	c.limitsCacheTTL = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = c.Admit(ctx, fmt.Sprintf("crew-%d", i), "s", nil)
		}(i)
	}
	wg.Wait()

	// 8 waiters polling every 1ms for 250ms is ~2000 opportunities. With a
	// 500ms cache the resolver must be called a handful of times at most.
	if n := resolved.Load(); n > 20 {
		t.Fatalf("limits resolver called %d times in 250ms across 8 waiters — it is being re-read on every poll", n)
	}
	if resolved.Load() == 0 {
		t.Fatal("limits resolver never called")
	}
}

// Cached, not frozen: an operator who moves a setting must see it take effect
// without restarting the daemon.
func TestAdmit_LimitsAreRefreshedAfterTheCacheWindow(t *testing.T) {
	var required atomic.Int64
	required.Store(4096)
	avail := int64(1000)
	c := New(func(context.Context) Limits {
		return Limits{RequiredFreeMB: required.Load()}
	}, memReader(&avail, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.pollInterval = 2 * time.Millisecond
	c.hostCacheTTL = time.Millisecond
	c.limitsCacheTTL = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	admitted := make(chan struct{})
	go func() {
		if release, err := c.Admit(ctx, "crew-a", "alpha", nil); err == nil {
			release()
			close(admitted)
		}
	}()

	select {
	case <-admitted:
		t.Fatal("admitted against a 4096 MiB floor with 1000 MiB available")
	case <-time.After(40 * time.Millisecond):
	}

	// The operator lowers the floor. No restart.
	required.Store(512)
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("a lowered memory floor never took effect — the limits cache never expires")
	}
}
