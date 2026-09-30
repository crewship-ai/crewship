// Package quiesce is the process-wide "hold still" switch the instance
// backup uses to take a consistent copy, and the persisted automation holds
// an instance restore leaves behind.
//
// Two separate things live here, because they answer two separate questions:
//
//   - The quiet window (Controller) — "is a consistent copy being taken right
//     now?". It is in memory only: a process that crashes mid-window comes
//     back with nothing held, which is the point. Begin drains running work
//     first (up to a busy wait), then closes admission to new writers, waits
//     for every writer already inside to leave (up to a drain timeout), and
//     only then holds — the copy starts on a boundary no write straddles. A
//     hard cap releases the window on its own if the copy runs long. Packing
//     and encrypting happen after Release.
//
//     Writers are everything that changes protected data: each mutating HTTP
//     request, each sweep, dispatcher claim and scheduler fire. They register
//     with Enter / EnterWait and Leave when done (writers.go); that count is
//     the barrier. A predicate checked once on the way in is not one — a
//     request admitted a moment before the window keeps writing inside it.
//
//   - Holds (Holds) — "did an instance restore leave automations stopped?".
//     Persisted in instance_holds by `crewship recover`, read at boot, and
//     cleared one key at a time by an instance admin. While a key is held the
//     server does not fire schedules (routines), answers inbound webhooks with
//     503 (webhooks), or start queued work and external retries (queue).
//
// Everything that must respect either one asks a small predicate —
// RoutinesPaused, WebhooksPaused, QueuePaused, WritesHeld, Closing — and
// every writer enters the gate (Enter, EnterWait, the gates' Enter). The
// package imports nothing from the rest of Crewship so any package can use it.
package quiesce

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Defaults from the backup plan (PLAN.md §Data model: busy_wait_minutes 120,
// hold_cap_minutes 20).
const (
	DefaultBusyWait = 120 * time.Minute
	DefaultHoldCap  = 20 * time.Minute
	// DefaultDrainTimeout bounds the wait for writers already inside the
	// gate once admission closes. Ordinary requests finish in well under a
	// second; one still running after a minute is stuck or streaming, and
	// the window is abandoned rather than copied around it.
	DefaultDrainTimeout = 60 * time.Second
	// DrainTimeoutEnv overrides DefaultDrainTimeout (a Go duration: "90s").
	DrainTimeoutEnv = "CREWSHIP_BACKUP_DRAIN_TIMEOUT"
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
	// ErrWritesDidNotDrain: writers admitted before the window closed were
	// still inside when the drain timeout ran out. The window is released
	// without a copy. The error Begin returns also matches ErrBusy, so every
	// caller that records a busy run as skipped records this one the same
	// way.
	ErrWritesDidNotDrain = errors.New("quiesce: writes did not drain")
)

// drainError is what Begin returns when the writer drain times out.
type drainError struct {
	n       int
	timeout time.Duration
}

func (e *drainError) Error() string {
	return fmt.Sprintf("quiesce: writes did not drain: %d still in flight after %s", e.n, e.timeout)
}

func (e *drainError) Is(target error) bool {
	return target == ErrWritesDidNotDrain || target == ErrBusy
}

// BusyFunc reports how much work is running and a short description of it
// ("2 agent runs"). Zero means drained.
type BusyFunc func(ctx context.Context) (n int, what string, err error)

// Options configure one Begin.
type Options struct {
	// BusyWait is how long Begin waits for running work to finish before
	// giving up with ErrBusy. Zero means DefaultBusyWait.
	BusyWait time.Duration
	// HoldCap is the longest the window may stay held once writers have
	// drained. Zero means DefaultHoldCap.
	HoldCap time.Duration
	// DrainTimeout bounds the wait for in-flight writers after admission
	// closes. Zero means $CREWSHIP_BACKUP_DRAIN_TIMEOUT, else
	// DefaultDrainTimeout. When it runs out Begin releases the window and
	// returns ErrWritesDidNotDrain.
	DrainTimeout time.Duration
	// Poll is how often Busy is asked during the drain. Zero means 2s.
	Poll time.Duration
	// Busy reports running work. Nil means nothing is ever busy.
	Busy BusyFunc
	// Acquire runs once writers have drained and running work reads zero —
	// the caller's own guards (per-workspace backup guards). An error puts
	// Begin back into the drain, so a run that slipped in between the last
	// busy check and the close is waited for, not copied half-done.
	Acquire func() (release func(), err error)
	// Reason is shown to callers that hit the hold (logs, 503 bodies).
	Reason string
}

// Controller is the quiet-window switch. The zero value is not usable; use
// New or Default.
//
// A window moves through three states: open (no window; writers are
// admitted), closing (admission closed; waiting for the writers already
// inside to leave) and held (writers drained; the copy runs). Release, the
// hard cap or a failed drain return it to open. Nothing is persisted: a
// crash in any state comes back open.
type Controller struct {
	mu       sync.Mutex
	window   *Window       // non-nil while closing or held
	released chan struct{} // closed when no window is open
	writers  int           // writers inside the gate
	drained  chan struct{} // while closing with writers inside: the window's drained, closed by the last Leave
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

// Holding reports whether a quiet window is open — closing or held. New
// writers are refused while it reads true.
func (c *Controller) Holding() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.window != nil
}

// Held reports whether the window has drained its writers and holds: the
// consistent copy may run. False while closing.
func (c *Controller) Held() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.window != nil && c.window.held
}

// Writers is the number of writers inside the gate right now.
func (c *Controller) Writers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writers
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
// until the hard cap (while closing: drain timeout plus hard cap), at least
// one second. Zero when no window is open.
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
// or ctx ends. The wait is bounded by the drain timeout plus the hard cap.
// Writers use EnterWait, which waits the same way and then registers.
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

// Begin drains running work, closes admission, waits for the writers already
// inside, and returns a held window. It returns ErrBusy when running work did
// not finish within opts.BusyWait, ErrWritesDidNotDrain (which also matches
// ErrBusy) when writers were still inside after opts.DrainTimeout,
// ErrAlreadyHeld when a window is open, and ctx's error when ctx ends first.
// On every error nothing is held and admission is open again. The returned
// Window must be released; its Context ends when the hard cap fires.
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
	drainTimeout := opts.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = drainTimeoutFromEnv()
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
			w, err := c.close(opts, drainTimeout+holdCap)
			if err != nil {
				return nil, err
			}
			// The barrier: nobody new gets in, and everyone already in
			// finishes before the copy may start.
			if err := c.drain(ctx, w, drainTimeout); err != nil {
				w.Release()
				return nil, err
			}
			// Work that started between the busy check and the close (a
			// writer that started a run, say) is waited for: check again
			// with admission closed and the writers gone.
			n2, what2, err := busyCount(ctx, opts.Busy)
			if err != nil {
				w.Release()
				return nil, err
			}
			if n2 == 0 {
				var rel func()
				var aerr error
				if opts.Acquire != nil {
					rel, aerr = opts.Acquire()
				}
				if aerr == nil {
					w.addRelease(rel)
					w.hold(holdCap)
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

func drainTimeoutFromEnv() time.Duration {
	if v := os.Getenv(DrainTimeoutEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultDrainTimeout
}

// close opens a window in the closing state: from here on Enter refuses, and
// sweeps waiting in EnterWait keep waiting. retryIn seeds the Retry-After
// hint until the window holds (drain timeout + hard cap).
func (c *Controller) close(opts Options, retryIn time.Duration) (*Window, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.window != nil {
		return nil, ErrAlreadyHeld
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	w := &Window{
		c:        c,
		reason:   opts.Reason,
		started:  now,
		deadline: now.Add(retryIn),
		ctx:      ctx,
		cancel:   cancel,
	}
	c.window = w
	c.released = make(chan struct{})
	w.drained = make(chan struct{})
	if c.writers == 0 {
		close(w.drained)
	} else {
		c.drained = w.drained
	}
	return w, nil
}

// drain waits until no writer is inside the gate, bounded by timeout.
func (c *Controller) drain(ctx context.Context, w *Window, timeout time.Duration) error {
	ch := w.drained
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		// Released from outside while draining.
		return context.Canceled
	case <-t.C:
		c.mu.Lock()
		n := c.writers
		c.mu.Unlock()
		if n == 0 {
			return nil
		}
		return &drainError{n: n, timeout: timeout}
	}
}

// Window is one open quiet window.
type Window struct {
	c        *Controller
	reason   string
	started  time.Time
	deadline time.Time // guarded by c.mu
	ctx      context.Context
	cancel   context.CancelFunc
	drained  chan struct{} // closed once no writer is inside

	mu       sync.Mutex
	timer    *time.Timer
	done     bool
	expired  bool
	heldFor  time.Duration
	releases []func()

	held bool // guarded by c.mu: writers drained, the copy may run
}

// hold moves a drained window to held and starts the hard cap.
func (w *Window) hold(holdCap time.Duration) {
	c := w.c
	c.mu.Lock()
	if c.window == w {
		w.held = true
		w.deadline = time.Now().Add(holdCap)
	}
	c.mu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	w.timer = time.AfterFunc(holdCap, func() {
		w.mu.Lock()
		w.expired = true
		w.mu.Unlock()
		w.Release()
	})
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

// Release closes the window (idempotent), reopens admission to writers and
// returns how long writers were refused (closing plus held).
func (w *Window) Release() time.Duration {
	w.mu.Lock()
	if w.done {
		held := w.heldFor
		w.mu.Unlock()
		return held
	}
	w.done = true
	w.heldFor = time.Since(w.started)
	releases := w.releases
	w.releases = nil
	held := w.heldFor
	timer := w.timer
	w.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	w.cancel()
	for i := len(releases) - 1; i >= 0; i-- {
		releases[i]()
	}
	c := w.c
	c.mu.Lock()
	if c.window == w {
		c.window = nil
		c.drained = nil
		w.held = false
		close(c.released)
	}
	c.mu.Unlock()
	return held
}

// Package-level predicates over the default controller and holds. These are
// what the hooks call.

// WritesHeld reports whether the consistent copy may be running: the window
// has drained every writer and holds. A writer inside the gate never sees it
// true — the window waits for it to leave first.
func WritesHeld() bool { return Default().Held() }

// Closing reports whether admission is closed (a window is closing or held).
// A long unit of work inside the gate checks it between batches and stops or
// yields (Writer.Yield), so the drain it is holding up can finish.
func Closing() bool { return Default().Holding() }

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

// EnterGate is a Gate whose passes can also register as a writer: Enter
// reads the gate and, when it would pass, enters the default controller.
type EnterGate interface {
	Gate
	Enter() (leave func(), ok bool)
}

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

// Enter is IsLeader plus admission: ok only when the gate passes AND the
// default controller admits a writer. The caller runs its whole fire inside
// and calls leave when done.
func (g pausedGate) Enter() (func(), bool) {
	if !g.IsLeader() {
		return func() {}, false
	}
	w, ok := Default().Enter(context.Background())
	if !ok {
		return func() {}, false
	}
	return w.Leave, true
}

// SchedulerGate wraps a scheduler's leader gate (nil: no election) so it also
// reads "not now" while routines are paused. A due occurrence stays due and
// fires on the first tick after the pause ends — the schedulers' overdue
// sweeps already handle that. Its Enter registers the fire as a writer.
func SchedulerGate(inner Gate) Gate { return pausedGate{inner: inner, paused: RoutinesPaused} }

// QueueGate is SchedulerGate for queue consumers (notification retries).
func QueueGate(inner Gate) Gate { return pausedGate{inner: inner, paused: QueuePaused} }

// EnterVia passes a gate the way a writer must: through the gate's Enter
// when it has one (SchedulerGate, QueueGate), else by IsLeader alone with
// nothing to leave. A nil gate always passes.
func EnterVia(g Gate) (leave func(), ok bool) {
	if eg, isEnter := g.(EnterGate); isEnter {
		return eg.Enter()
	}
	if g != nil && !g.IsLeader() {
		return func() {}, false
	}
	return func() {}, true
}

// AdmitQueue is the admission a queue consumer (a dispatcher claiming work)
// passes before each claim: not paused by the queue hold or a window, and
// registered as a writer. leave ends the writer.
func AdmitQueue() (leave func(), ok bool) {
	if DefaultHolds().Held(HoldQueue) {
		return func() {}, false
	}
	w, ok := Default().Enter(context.Background())
	if !ok {
		return func() {}, false
	}
	return w.Leave, true
}
