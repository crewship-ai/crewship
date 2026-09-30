// Package quiesce is the process-wide "hold still" switch the instance
// backup uses to take a consistent copy, and the persisted automation holds
// an instance restore leaves behind.
//
// Two separate things live here, because they answer two separate questions:
//
//   - The quiet window (Controller) — "is a consistent copy being taken right
//     now?". It is in memory only: a process that crashes mid-window comes
//     back with nothing held, which is the point. Begin drains running work
//     first (up to a busy wait), then holds every write to protected data
//     until Release, and a hard cap releases it on its own if the copy runs
//     long. Packing and encrypting happen after Release.
//
//   - Holds (Holds) — "did an instance restore leave automations stopped?".
//     Persisted in instance_holds by `crewship recover`, read at boot, and
//     cleared one key at a time by an instance admin. While a key is held the
//     server does not fire schedules (routines), answers inbound webhooks with
//     503 (webhooks), or start queued work and external retries (queue).
//
// Everything that must respect either one asks a small predicate —
// RoutinesPaused, WebhooksPaused, QueuePaused, WritesHeld — or waits with
// WaitReleased before a sweep. The package imports nothing from the rest of
// Crewship so any package can check it.
package quiesce

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Defaults from the backup plan (PLAN.md §Data model: busy_wait_minutes 120,
// hold_cap_minutes 20).
const (
	DefaultBusyWait = 120 * time.Minute
	DefaultHoldCap  = 20 * time.Minute
	defaultPoll     = 2 * time.Second
)

var (
	// ErrBusy: running work did not finish within the busy wait. The run is
	// recorded as skipped, never forced.
	ErrBusy = errors.New("quiesce: work was still running when the busy wait ran out")
	// ErrAlreadyHeld: another quiet window is open in this process.
	ErrAlreadyHeld = errors.New("quiesce: a quiet window is already open")
	// ErrHoldCapExceeded: the copy did not finish inside the hard cap, so the
	// window released itself and the copy must be abandoned.
	ErrHoldCapExceeded = errors.New("quiesce: the hold cap ran out before the copy finished")
)

// BusyFunc reports how much work is running and a short description of it
// ("2 agent runs"). Zero means drained.
type BusyFunc func(ctx context.Context) (n int, what string, err error)

// Options configure one Begin.
type Options struct {
	// BusyWait is how long Begin waits for running work to finish before
	// giving up with ErrBusy. Zero means DefaultBusyWait.
	BusyWait time.Duration
	// HoldCap is the longest the window may stay open. Zero means
	// DefaultHoldCap.
	HoldCap time.Duration
	// Poll is how often Busy is asked during the drain. Zero means 2s.
	Poll time.Duration
	// Busy reports running work. Nil means nothing is ever busy.
	Busy BusyFunc
	// Acquire runs once the hold is in place and running work reads zero —
	// the caller's own guards (per-workspace backup guards). An error puts
	// Begin back into the drain, so a run that slipped in between the last
	// busy check and the hold is waited for, not copied half-done.
	Acquire func() (release func(), err error)
	// Reason is shown to callers that hit the hold (logs, 503 bodies).
	Reason string
}

// Controller is the quiet-window switch. The zero value is not usable; use
// New or Default.
type Controller struct {
	mu       sync.Mutex
	window   *Window
	released chan struct{} // closed when no window is open
}

// New returns a controller with no window open.
func New() *Controller {
	c := &Controller{released: make(chan struct{})}
	close(c.released)
	return c
}

var defaultController = New()

// Default is the process-wide controller every hook consults.
func Default() *Controller { return defaultController }

// Holding reports whether a quiet window is open.
func (c *Controller) Holding() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.window != nil
}

// Reason is the open window's reason, or "".
func (c *Controller) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window == nil {
		return ""
	}
	return c.window.reason
}

// RetryAfter is a hint for a caller refused by the window: the time left
// until the hard cap, at least one second. Zero when no window is open.
func (c *Controller) RetryAfter() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window == nil {
		return 0
	}
	left := time.Until(c.window.deadline)
	if left < time.Second {
		left = time.Second
	}
	return left
}

// WaitReleased blocks until no window is open (returns at once when none is),
// or ctx ends. Sweepers call it before each pass so a retention or GC sweep
// never deletes under a copy. The wait is bounded by the window's hard cap.
func (c *Controller) WaitReleased(ctx context.Context) error {
	c.mu.Lock()
	ch := c.released
	c.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Begin drains, then opens the window. It returns ErrBusy when running work
// did not finish within opts.BusyWait, ErrAlreadyHeld when a window is open,
// and ctx's error when ctx ends first. The returned Window must be released;
// its Context ends when the hard cap fires.
func (c *Controller) Begin(ctx context.Context, opts Options) (*Window, error) {
	busyWait := opts.BusyWait
	if busyWait <= 0 {
		busyWait = DefaultBusyWait
	}
	holdCap := opts.HoldCap
	if holdCap <= 0 {
		holdCap = DefaultHoldCap
	}
	poll := opts.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	if c.Holding() {
		return nil, ErrAlreadyHeld
	}
	deadline := time.Now().Add(busyWait)
	lastWhat := ""
	for {
		n, what, err := busyCount(ctx, opts.Busy)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			w, err := c.open(opts, holdCap)
			if err != nil {
				return nil, err
			}
			// Work that started between the busy check and the hold is
			// waited for: check again with the hold in place.
			n2, what2, err := busyCount(ctx, opts.Busy)
			if err != nil {
				w.Release()
				return nil, err
			}
			if n2 == 0 {
				if opts.Acquire == nil {
					return w, nil
				}
				rel, aerr := opts.Acquire()
				if aerr == nil {
					w.addRelease(rel)
					return w, nil
				}
				what2 = aerr.Error()
			}
			w.Release()
			lastWhat = what2
		} else {
			lastWhat = what
		}
		if !time.Now().Before(deadline) {
			if lastWhat != "" {
				return nil, fmt.Errorf("%w (%s)", ErrBusy, lastWhat)
			}
			return nil, ErrBusy
		}
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func busyCount(ctx context.Context, f BusyFunc) (int, string, error) {
	if f == nil {
		return 0, "", nil
	}
	return f(ctx)
}

func (c *Controller) open(opts Options, holdCap time.Duration) (*Window, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window != nil {
		return nil, ErrAlreadyHeld
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Window{
		c:        c,
		reason:   opts.Reason,
		started:  time.Now(),
		deadline: time.Now().Add(holdCap),
		ctx:      ctx,
		cancel:   cancel,
	}
	w.timer = time.AfterFunc(holdCap, func() {
		w.mu.Lock()
		w.expired = true
		w.mu.Unlock()
		w.Release()
	})
	c.window = w
	c.released = make(chan struct{})
	return w, nil
}

// Window is one open quiet window.
type Window struct {
	c        *Controller
	reason   string
	started  time.Time
	deadline time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	timer    *time.Timer

	mu       sync.Mutex
	done     bool
	expired  bool
	held     time.Duration
	releases []func()
}

func (w *Window) addRelease(f func()) {
	if f == nil {
		return
	}
	w.mu.Lock()
	w.releases = append(w.releases, f)
	w.mu.Unlock()
}

// Context ends when the window is released — by Release, or by the hard cap.
// The copy runs under it so the cap can stop it.
func (w *Window) Context() context.Context { return w.ctx }

// Expired reports whether the hard cap released the window.
func (w *Window) Expired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.expired
}

// Release closes the window (idempotent) and returns how long it was held.
func (w *Window) Release() time.Duration {
	w.mu.Lock()
	if w.done {
		held := w.held
		w.mu.Unlock()
		return held
	}
	w.done = true
	w.held = time.Since(w.started)
	releases := w.releases
	w.releases = nil
	held := w.held
	w.mu.Unlock()

	w.timer.Stop()
	w.cancel()
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
	c := w.c
	c.mu.Lock()
	if c.window == w {
		c.window = nil
		close(c.released)
	}
	c.mu.Unlock()
	return held
}

// Package-level predicates over the default controller and holds. These are
// what the hooks call.

// WritesHeld reports whether mutating requests to protected data must wait.
func WritesHeld() bool { return Default().Holding() }

// RoutinesPaused reports whether schedulers must not fire: a quiet window is
// open, or an instance restore left routines held.
func RoutinesPaused() bool { return Default().Holding() || DefaultHolds().Held(HoldRoutines) }

// WebhooksPaused reports whether inbound webhooks must be refused with 503.
func WebhooksPaused() bool { return Default().Holding() || DefaultHolds().Held(HoldWebhooks) }

// QueuePaused reports whether queued work and external retries must not start.
func QueuePaused() bool { return Default().Holding() || DefaultHolds().Held(HoldQueue) }

// WaitReleased waits for the default controller's window to close.
func WaitReleased(ctx context.Context) error { return Default().WaitReleased(ctx) }

// Gate is the scheduler leader gate's shape (internal/leader.Gate).
type Gate interface{ IsLeader() bool }

type pausedGate struct {
	inner  Gate
	paused func() bool
}

func (g pausedGate) IsLeader() bool {
	if g.paused() {
		return false
	}
	return g.inner == nil || g.inner.IsLeader()
}

// SchedulerGate wraps a scheduler's leader gate (nil: no election) so it also
// reads "not now" while routines are paused. A due occurrence stays due and
// fires on the first tick after the pause ends — the schedulers' overdue
// sweeps already handle that.
func SchedulerGate(inner Gate) Gate { return pausedGate{inner: inner, paused: RoutinesPaused} }

// QueueGate is SchedulerGate for queue consumers (notification retries).
func QueueGate(inner Gate) Gate { return pausedGate{inner: inner, paused: QueuePaused} }
