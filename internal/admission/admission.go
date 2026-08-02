// Package admission holds a crew container start until the host can afford
// it, and makes the wait visible.
//
// # What this is not
//
// It is not "pause the agent and resume it when memory frees". That cannot be
// built: `docker pause` is a single write to cgroup.freeze — runc's Pause()
// touches nothing else — so it frees no memory, the cgroup keeps every page,
// and moby still reports State.Running. Docker exposes no memory.high either,
// the kernel's throttle-instead-of-kill lever. The achievable form of the same
// behaviour is one step earlier: hold the work BEFORE the container exists.
// Nothing here ever touches a running container.
//
// It is also not a resource manager. No cgroup tuning, no eviction, no
// rebalancing. The only question it answers is "should this host start one
// more crew container right now, or wait".
//
// # Why it lives below the call sites
//
// The pre-existing bound, the orchestrator's runSem, is acquired inside
// RunAgent — but every one of RunAgent's callers has already created and
// started its container by then, so container creates were unbounded. The
// obvious repair is a gate the callers invoke first, and it is the wrong one:
// a gate that eleven call sites must remember to call is the same class of
// defect as the one being fixed, and a twelfth caller lands next month. So the
// gate is invoked by the two providers, at the exact statements that make a
// container resident (ContainerCreate, and ContainerStart of a stopped
// container). Warm reuse of a running container adds nothing to the host and
// pays nothing here.
//
// # On hosts without the signal
//
// MemAvailable and PSI are Linux. On macOS — where the Apple provider runs —
// neither file exists, and this package DOES NOT gate on memory there. It says
// so on the status surface rather than pretending: HostSignalAvailable is
// false and HostSignalError carries the reason. The two legs that need no
// kernel file, the concurrency bound and the stagger, still apply, because
// they protect the container runtime rather than the host's RAM.
package admission

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Reasons a start is being held. Stable strings: they reach the API, the CLI
// and the run's own provisioning event stream.
const (
	ReasonHostMemory   = "host_memory"
	ReasonHostPressure = "host_pressure"
	ReasonConcurrency  = "concurrency"
	ReasonPacing       = "pacing"
)

// ReasonPhrase renders a hold reason as a clause a person can read, without
// the reason token. Exported because two surfaces need the same words and
// must not drift: the live "waiting for capacity" line on the run's own
// stream while it waits, and the failure message when the wait runs out.
func ReasonPhrase(reason string) string {
	switch reason {
	case ReasonHostMemory:
		return "the host does not have enough free memory to start another agent container"
	case ReasonHostPressure:
		return "the host is already stalling on memory"
	case ReasonConcurrency:
		return "too many agent containers are starting on this host at once"
	case ReasonPacing:
		return "agent container starts are being staggered"
	default:
		return "this host cannot afford another agent container yet"
	}
}

// ReasonRemedy names what an operator can actually change. Empty when there
// is nothing useful to say — a pacing hold clears itself in milliseconds and
// telling somebody to reconfigure it would be noise.
func ReasonRemedy(reason string) string {
	switch reason {
	case ReasonHostMemory:
		return "Free memory on the host, lower the crew's container memory, " +
			"or lower runtime.host_memory_reserve_mb."
	case ReasonHostPressure:
		return "Reduce load on the host, or raise runtime.host_memory_pressure_pct."
	case ReasonConcurrency:
		return "Wait for the starts in flight to finish, or raise runtime.max_concurrent_container_starts."
	default:
		return ""
	}
}

// ErrHeldForCapacity marks every failure produced by a start that was held
// until its deadline. Callers classify against it with errors.Is rather than
// by matching text: the previous arrangement was a substring classifier, and
// the gate's own wrapper contains the substring "container start", so the
// generic provisioning case claimed the capacity failure and threw away the
// only detail an operator could act on (#1675).
var ErrHeldForCapacity = errors.New("held for host capacity")

// HoldExpiredError is what Admit returns when the caller's context ran out
// while the start was still held. It carries the binding reason, the numbers
// behind it and how long the start waited, so the surface reporting it can
// name the host resource that ran out instead of saying "provisioning error".
type HoldExpiredError struct {
	CrewID   string
	CrewSlug string
	// Reason is one of the Reason* constants.
	Reason string
	// Detail is the human-readable numbers for Reason, e.g. "host has 41837
	// MiB available, 62048 MiB needed for one more agent container".
	Detail string
	// Waited is how long the start was held before it gave up.
	Waited time.Duration
	// Err is the context error that ended the wait.
	Err error
}

func (e *HoldExpiredError) Error() string {
	return fmt.Sprintf("held for host capacity for %s (%s: %s): %v",
		e.Waited.Round(time.Millisecond), e.Reason, e.Detail, e.Err)
}

// Unwrap reports BOTH the sentinel and the context error that ended the wait,
// so errors.Is(err, ErrHeldForCapacity) and errors.Is(err,
// context.DeadlineExceeded) hold on the same value. Dropping the second would
// break callers that already distinguish a cancelled run from an expired one.
func (e *HoldExpiredError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrHeldForCapacity}
	}
	return []error{ErrHeldForCapacity, e.Err}
}

// Limits is the resolved admission policy. Every field is "0 disables this
// leg", so an operator can turn off one check without losing the others.
type Limits struct {
	// MaxConcurrentStarts bounds container creates/starts in flight.
	MaxConcurrentStarts int
	// MinStartInterval is the minimum spacing between two admissions — the
	// stagger.
	MinStartInterval time.Duration
	// RequiredFreeMB is how much host memory must remain available for a
	// start to be admitted.
	RequiredFreeMB int64
	// MaxPressurePct is the PSI "some avg10" share above which the host is
	// judged to be already stalling on memory.
	MaxPressurePct float64
}

// LimitsResolver reads the policy fresh for each decision, so an operator's
// `crewship instance settings set` takes effect on the next held start rather
// than on the next daemon restart.
type LimitsResolver func(ctx context.Context) Limits

// HostReader returns one reading of host memory headroom. Errors wrapping
// ErrHostSignalUnavailable stand the memory gate down; any other error is
// treated the same way but logged, because a gate that fails closed on a
// transient read error would wedge every run on the instance.
type HostReader func() (HostMemory, error)

// Hold describes one start that is currently waiting.
type Hold struct {
	CrewID   string    `json:"crew_id"`
	CrewSlug string    `json:"crew_slug,omitempty"`
	Reason   string    `json:"reason"`
	Detail   string    `json:"detail,omitempty"`
	Since    time.Time `json:"since"`
	WaitedMs int64     `json:"waited_ms"`
}

// Snapshot is the whole status surface: what the policy is, what the host
// says, and who is waiting.
type Snapshot struct {
	Limits              Limits     `json:"limits"`
	InFlightStarts      int        `json:"in_flight_starts"`
	Held                []Hold     `json:"held"`
	HeldTotal           uint64     `json:"held_total"`
	HostSignalAvailable bool       `json:"host_signal_available"`
	HostSignalError     string     `json:"host_signal_error,omitempty"`
	Host                HostMemory `json:"host,omitzero"`
}

// Controller is the gate. The zero value is not usable; use New. A nil
// *Controller is usable and is a pass-through.
type Controller struct {
	limits LimitsResolver
	read   HostReader
	logger *slog.Logger

	// pollInterval is how often a held start re-reads the world. Container
	// starts are a slow path and nothing in the kernel will tell us memory
	// freed, so polling is the honest implementation; a release also
	// broadcasts, so the concurrency leg does not pay the poll latency.
	pollInterval time.Duration
	// hostCacheTTL collapses the host read across concurrent waiters. Without
	// it, twenty held starts would each read /proc twenty times a second.
	hostCacheTTL time.Duration
	// limitsCacheTTL does the same for the policy read, which is the more
	// expensive of the two: resolving Limits is five indexed SELECTs against
	// the same SQLite file the runs themselves are using, and a twenty-crew
	// wake polling several times a second would turn that into hundreds of
	// queries a second. One second is short enough that an operator's
	// `crewship instance settings set` still lands on the next held start —
	// the property that made the settings live rather than boot-time — and
	// long enough that the poll loop costs nothing.
	limitsCacheTTL time.Duration
	// holdNotify is the gap to the next "still waiting" notice for a hold
	// whose reason has not changed. See defaultHoldNotify.
	holdNotify []time.Duration

	mu        sync.Mutex
	inFlight  int
	lastAdmit time.Time
	holds     map[uint64]*Hold
	nextID    uint64
	heldTotal uint64
	wake      chan struct{}

	hostAt  time.Time
	hostVal HostMemory
	hostErr error

	limitsAt  time.Time
	limitsVal Limits

	// signalWarned fires the "the memory gate is inactive on this host"
	// warning once per process rather than once per start.
	signalWarned sync.Once
}

const (
	defaultPollInterval   = 200 * time.Millisecond
	defaultHostCacheTTL   = 250 * time.Millisecond
	defaultLimitsCacheTTL = time.Second

	// holdNotifyFloor is the shortest gap between two notices, whatever else
	// happens. It exists for the reason that FLAPS: memory frees for one poll
	// and the concurrency bound binds instead, then back. A reason change is
	// worth reporting, but at 200ms per poll an unconditional "report every
	// change" would put five lines a second on the caller's stream.
	holdNotifyFloor = 5 * time.Second
)

// defaultHoldNotify is the gap to the next notice for a hold whose reason has
// not changed, escalating and then repeating on its last entry. Cumulatively:
// 0s, 30s, 1m, 2m, 4m, 9m, 19m, 29m — eight lines across the 30-minute run
// budget instead of the sixty a fixed 30s cadence would produce, with the
// close-together ones early, where somebody is still deciding whether the run
// has hung. A field on the Controller rather than a package var so tests can
// compress it without racing each other.
var defaultHoldNotify = []time.Duration{
	30 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
}

// New builds a Controller. limits and read may be nil: a nil resolver yields
// zero Limits (every leg disabled) and a nil reader yields no host signal, so
// a partially-wired Controller degrades rather than panics.
func New(limits LimitsResolver, read HostReader, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Controller{
		limits:         limits,
		read:           read,
		logger:         logger,
		pollInterval:   defaultPollInterval,
		hostCacheTTL:   defaultHostCacheTTL,
		limitsCacheTTL: defaultLimitsCacheTTL,
		holdNotify:     defaultHoldNotify,
		holds:          make(map[uint64]*Hold),
		wake:           make(chan struct{}),
	}
}

// Admit blocks until this host can afford one more crew container, then
// returns the release. The release must be called on every exit path; it is
// idempotent.
//
// onHold, when non-nil, is called the first time the start is actually held,
// whenever the binding reason changes, and then on an escalating schedule for
// as long as the wait lasts (see defaultHoldNotify). It is how a run's own
// event stream says "waiting for capacity" instead of going quiet — a silent
// queue is indistinguishable from a hang, which is how this feature would earn
// a bug report instead of trust.
//
// Reporting only on a reason CHANGE was not enough: the common hold has one
// reason for its whole life, so the caller got one line and then thirty
// minutes of silence. Reporting on every poll is the other failure, so the
// notices are rate-limited — never closer together than holdNotifyFloor, and
// spreading out as the wait goes on.
//
// It runs on the caller's goroutine; keep it cheap.
func (c *Controller) Admit(ctx context.Context, crewID, crewSlug string, onHold func(reason, detail string)) (func(), error) {
	if c == nil {
		return func() {}, nil
	}

	var (
		holdID     uint64
		registered bool
		lastReason string
		notifiedAt time.Time
		notices    int
	)
	defer func() {
		if registered {
			c.forget(holdID)
		}
	}()

	for {
		lim := c.resolveLimits(ctx)
		ok, reason, detail := c.tryAcquire(lim)
		if ok {
			if registered {
				c.logger.Info("crew container start admitted after a capacity hold",
					"crew_id", crewID, "held_for", time.Since(c.holdSince(holdID)).Round(time.Millisecond))
			}
			return c.releaser(), nil
		}

		if !registered {
			holdID = c.remember(crewID, crewSlug, reason, detail)
			registered = true
			c.logger.Info("holding crew container start for capacity",
				"crew_id", crewID, "crew_slug", crewSlug, "reason", reason, "detail", detail)
		} else if reason != lastReason {
			c.updateHold(holdID, reason, detail)
		}
		if onHold != nil && c.notifyDue(notifiedAt, notices, reason != lastReason) {
			onHold(reason, detail)
			notifiedAt = time.Now()
			notices++
		}
		lastReason = reason

		wake := c.wakeChan()
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &HoldExpiredError{
				CrewID:   crewID,
				CrewSlug: crewSlug,
				Reason:   reason,
				Detail:   detail,
				Waited:   time.Since(c.holdSince(holdID)),
				Err:      ctx.Err(),
			}
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// notifyDue answers whether the caller should be told about the hold on this
// poll. notices is how many have already gone out; the gap after the n-th is
// holdNotify[n-1], with the last entry repeating for the rest of the wait.
// The first notice is immediate. Any gap is brought forward to holdNotifyFloor
// when the binding reason has changed — a change is news, but not news worth
// five lines a second when two legs are trading the hold back and forth.
func (c *Controller) notifyDue(notifiedAt time.Time, notices int, reasonChanged bool) bool {
	if notices == 0 || notifiedAt.IsZero() {
		return true
	}
	due := holdNotifyFloor
	if n := len(c.holdNotify); n > 0 {
		due = c.holdNotify[min(notices-1, n-1)]
	}
	if reasonChanged && due > holdNotifyFloor {
		due = holdNotifyFloor
	}
	return time.Since(notifiedAt) >= due
}

// resolveLimits returns the policy, cached for limitsCacheTTL so N waiters
// polling M times a second do not become N*M settings reads.
func (c *Controller) resolveLimits(ctx context.Context) Limits {
	if c.limits == nil {
		return Limits{}
	}
	c.mu.Lock()
	if !c.limitsAt.IsZero() && time.Since(c.limitsAt) < c.limitsCacheTTL {
		lim := c.limitsVal
		c.mu.Unlock()
		return lim
	}
	c.mu.Unlock()

	// Resolved outside the mutex: it reads the database, and holding the
	// controller's lock across a query would serialise every release and
	// every Snapshot behind it.
	lim := c.limits(ctx)

	c.mu.Lock()
	c.limitsVal, c.limitsAt = lim, time.Now()
	c.mu.Unlock()
	return lim
}

// tryAcquire takes a slot if every enabled leg allows it. Memory is checked
// first because it is the leg an operator can act on, so it is the reason
// worth reporting when more than one applies.
func (c *Controller) tryAcquire(lim Limits) (ok bool, reason, detail string) {
	if lim.RequiredFreeMB > 0 || lim.MaxPressurePct > 0 {
		hm, err := c.hostMemory()
		switch {
		case err != nil:
			// No readable signal: the memory legs stand down. Fail-open is
			// deliberate and is the macOS answer — gating on a number we
			// cannot read would hold every start on the instance forever.
			c.warnSignalUnavailable(err)
		default:
			if lim.RequiredFreeMB > 0 && hm.AvailableMB < lim.RequiredFreeMB {
				return false, ReasonHostMemory, fmt.Sprintf(
					"host has %d MiB available, %d MiB needed for one more agent container",
					hm.AvailableMB, lim.RequiredFreeMB)
			}
			if lim.MaxPressurePct > 0 && hm.SomeStallPct != PressureUnknown && hm.SomeStallPct > lim.MaxPressurePct {
				return false, ReasonHostPressure, fmt.Sprintf(
					"host memory pressure (some avg10) is %.2f%%, above the %.2f%% ceiling",
					hm.SomeStallPct, lim.MaxPressurePct)
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if lim.MaxConcurrentStarts > 0 && c.inFlight >= lim.MaxConcurrentStarts {
		return false, ReasonConcurrency, fmt.Sprintf(
			"%d container starts already in flight, limit %d", c.inFlight, lim.MaxConcurrentStarts)
	}
	if lim.MinStartInterval > 0 && !c.lastAdmit.IsZero() {
		if wait := lim.MinStartInterval - time.Since(c.lastAdmit); wait > 0 {
			return false, ReasonPacing, fmt.Sprintf(
				"staggering simultaneous starts; next slot in %s", wait.Round(time.Millisecond))
		}
	}
	c.inFlight++
	c.lastAdmit = time.Now()
	return true, "", ""
}

// hostMemory serves a short-lived cached reading so N concurrent waiters cost
// one /proc read per TTL rather than N per poll.
func (c *Controller) hostMemory() (HostMemory, error) {
	c.mu.Lock()
	if !c.hostAt.IsZero() && time.Since(c.hostAt) < c.hostCacheTTL {
		hm, err := c.hostVal, c.hostErr
		c.mu.Unlock()
		return hm, err
	}
	read := c.read
	c.mu.Unlock()

	var (
		hm  HostMemory
		err error
	)
	if read == nil {
		err = ErrHostSignalUnavailable
	} else {
		hm, err = read()
	}

	c.mu.Lock()
	c.hostVal, c.hostErr, c.hostAt = hm, err, time.Now()
	c.mu.Unlock()
	return hm, err
}

func (c *Controller) warnSignalUnavailable(err error) {
	c.signalWarned.Do(func() {
		msg := "host memory admission gate is inactive: this platform does not publish /proc/meminfo, " +
			"so container starts are bounded and staggered but not gated on host memory"
		if !errors.Is(err, ErrHostSignalUnavailable) {
			msg = "host memory admission gate is inactive: the host signal could not be read"
		}
		c.logger.Warn(msg, "error", err)
	})
}

func (c *Controller) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.inFlight > 0 {
				c.inFlight--
			}
			c.mu.Unlock()
			c.broadcast()
		})
	}
}

// broadcast wakes every waiter by closing and replacing the wake channel.
// Cheaper than a poll for the concurrency leg, which is the one that changes
// on an event we control.
func (c *Controller) broadcast() {
	c.mu.Lock()
	old := c.wake
	c.wake = make(chan struct{})
	c.mu.Unlock()
	close(old)
}

func (c *Controller) wakeChan() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wake
}

func (c *Controller) remember(crewID, crewSlug, reason, detail string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	c.heldTotal++
	id := c.nextID
	c.holds[id] = &Hold{
		CrewID:   crewID,
		CrewSlug: crewSlug,
		Reason:   reason,
		Detail:   detail,
		Since:    time.Now(),
	}
	return id
}

func (c *Controller) updateHold(id uint64, reason, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.holds[id]; h != nil {
		h.Reason, h.Detail = reason, detail
	}
}

func (c *Controller) holdSince(id uint64) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.holds[id]; h != nil {
		return h.Since
	}
	return time.Now()
}

func (c *Controller) forget(id uint64) {
	c.mu.Lock()
	delete(c.holds, id)
	c.mu.Unlock()
}

// Snapshot reports the live state for the API and `crewship now`. Safe on a
// nil Controller, which reports an inactive gate rather than erroring.
func (c *Controller) Snapshot(ctx context.Context) Snapshot {
	if c == nil {
		return Snapshot{HostSignalError: "admission control not configured"}
	}
	snap := Snapshot{Limits: c.resolveLimits(ctx)}

	hm, err := c.hostMemory()
	if err != nil {
		snap.HostSignalError = err.Error()
	} else {
		snap.HostSignalAvailable = true
		snap.Host = hm
	}

	now := time.Now()
	c.mu.Lock()
	snap.InFlightStarts = c.inFlight
	snap.HeldTotal = c.heldTotal
	for _, h := range c.holds {
		cp := *h
		cp.WaitedMs = now.Sub(cp.Since).Milliseconds()
		snap.Held = append(snap.Held, cp)
	}
	c.mu.Unlock()

	sort.Slice(snap.Held, func(i, j int) bool {
		if snap.Held[i].Since.Equal(snap.Held[j].Since) {
			return snap.Held[i].CrewID < snap.Held[j].CrewID
		}
		return snap.Held[i].Since.Before(snap.Held[j].Since)
	})
	return snap
}
