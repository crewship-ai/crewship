package pages

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// The push rate limits (PRD §10b.3).
//
// The dangerous number in Pages is not size, it is FREQUENCY. §10b.3 does the
// arithmetic: 24 panels × 100 pages × one push every 5 s is 2 880 writes per
// second, and SQLite has exactly one writer. Nothing about the schema, the
// indexes or the ring changes that — only refusing the write does.
//
// So the rate is bounded TWICE, and the two layers are not redundant:
//
//  1. A token bucket per panel and per workspace, sized from
//     internal/ratelimitcfg so an operator can retune it in Settings without a
//     release. This is the layer that shapes ordinary traffic: it absorbs a
//     burst and then settles to the sustained rate.
//
//  2. A minimum interval between two stored payloads for one panel, checked
//     against produced_at IN THE SAME TRANSACTION AS THE INSERT. config/
//     rate-limits.yml's own header records why this exists — "MVP:
//     per-process, neskaluje přes více instancí". With N replicas every bucket
//     in layer 1 becomes N×; the floor is enforced by the database and is
//     therefore the same floor no matter how many processes are serving.
//
// Layer 2 is derived from layer 1's BURST rather than from its sustained rate,
// and that is the design decision in this file. A floor of one push per 5 s
// (the sustained 12/min read literally) would refuse the very burst the bucket
// exists to allow, so raising the burst would produce MORE 429s instead of
// fewer — a limiter that contradicts its own knob. Deriving it as
// window ÷ burst (60 s ÷ 30 = 2 s) makes the floor exactly as permissive as
// the bucket's most permissive steady state and never tighter, while still
// capping one panel at 30 writes/min against any number of replicas. The
// 2 880/s scenario collapses to at most 30/min/panel.
//
// Everything here takes its clock as an argument, like the rest of this
// package: a rate limit whose test has to sleep for a minute is a rate limit
// nobody tests at the boundary.

// rateWindow is the window every Pages rate is quoted against. Both configured
// rates are per minute; the public-view rate (per hour) is not enforced here
// because /p/{token} does not exist yet.
const rateWindow = time.Minute

// PushLimits is the set of numbers behind one push decision. They are read
// from the registry rather than declared here — the values are operational and
// belong in Settings, only their MEANING belongs in code.
type PushLimits struct {
	// PanelPerMin is the sustained rate for one panel.
	PanelPerMin int
	// PanelBurst is the bucket depth for one panel, and thus also the divisor
	// that sets MinInterval.
	PanelBurst int
	// WorkspacePerMin is the backstop across every panel in one workspace.
	// Per-panel limits do not compose: 100 pages × 24 panels each politely
	// under their own cap is still 2 880 writes a second.
	WorkspacePerMin int
}

// ConfiguredPushLimits reads the current values from the process-global
// limiter registry, falling back to the shipped defaults when no store is
// installed (CLI processes, unit tests) — so a limiter is never left reading
// zero.
func ConfiguredPushLimits() PushLimits {
	return PushLimits{
		PanelPerMin:     ratelimitcfg.Int(ratelimitcfg.KeyPagesPushPanelPerMin),
		PanelBurst:      ratelimitcfg.Int(ratelimitcfg.KeyPagesPushPanelBurst),
		WorkspacePerMin: ratelimitcfg.Int(ratelimitcfg.KeyPagesPushWSPerMin),
	}.sane()
}

// sane clamps every field to at least 1. The registry's own bounds already
// forbid zero, but a zero here would mean a bucket that never refills and a
// division by zero in MinInterval — i.e. a workspace whose panels can never be
// written again. Defence in depth against a limiter that fails CLOSED forever.
func (l PushLimits) sane() PushLimits {
	if l.PanelPerMin < 1 {
		l.PanelPerMin = 1
	}
	if l.PanelBurst < 1 {
		l.PanelBurst = 1
	}
	if l.WorkspacePerMin < 1 {
		l.WorkspacePerMin = 1
	}
	return l
}

// MinInterval is the floor between two STORED payloads for one panel — layer 2
// above. window ÷ burst, so it is never tighter than the bucket's most
// permissive steady state.
func (l PushLimits) MinInterval() time.Duration {
	l = l.sane()
	return rateWindow / time.Duration(l.PanelBurst)
}

// FloorCutoff is the timestamp a panel's newest stored payload must be at or
// before for another push to be admitted. The caller compares produced_at
// against it inside the push transaction, which is what makes the floor hold
// across processes.
func (l PushLimits) FloorCutoff(now time.Time) time.Time {
	return now.Add(-l.MinInterval())
}

// AdmitPush is the floor as a pure rule, for callers holding the panel's newest
// produced_at rather than a database handle. hasLast is false for a panel that
// has never been written, which is always admissible.
//
// The second return is how long the caller must wait, i.e. what belongs in
// Retry-After. It is zero when the push is admitted.
func (l PushLimits) AdmitPush(last time.Time, hasLast bool, now time.Time) (bool, time.Duration) {
	if !hasLast {
		return true, 0
	}
	interval := l.MinInterval()
	// A payload timestamped in the FUTURE relative to now cannot happen with
	// the server clock that writes it, but a restored backup or a clock step
	// can produce one. Treat it as "just written" rather than as a licence to
	// write immediately — the alternative lets a bad timestamp switch the floor
	// off entirely.
	elapsed := now.Sub(last)
	if elapsed >= interval {
		return true, 0
	}
	wait := interval - elapsed
	if wait > interval {
		wait = interval
	}
	return false, wait
}

// RetryAfterSeconds renders a wait as the integer second count Retry-After
// carries, rounded UP and never below 1 — a "Retry-After: 0" invites the
// immediate retry the header exists to prevent.
func RetryAfterSeconds(d time.Duration) int {
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	return secs
}

// LimitScope names which of the two buckets refused a push, so the 429 can say
// which limit was hit rather than leaving a producer to guess whether it is
// pushing one panel too fast or the whole workspace too fast.
type LimitScope string

const (
	// ScopePanel — the per-panel bucket or the database floor.
	ScopePanel LimitScope = "panel"
	// ScopeWorkspace — the workspace-wide backstop.
	ScopeWorkspace LimitScope = "workspace"
)

// PushLimiter holds layer 1: one token bucket per panel and one per workspace.
//
// It is per-process by construction and says so. Its job is to shape traffic
// and to answer quickly; the guarantee that survives replication is the floor,
// which lives in the database.
type PushLimiter struct {
	mu         sync.Mutex
	limits     PushLimits
	panels     map[string]*bucket
	workspaces map[string]*bucket
	lastSweep  time.Time
	// followsRegistry is set by NewConfiguredPushLimiter: the server's limiter
	// tracks the admin table, while a limiter built with explicit numbers (a
	// test, or a caller pinning a value on purpose) keeps the numbers it was
	// given.
	followsRegistry bool
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// idleBucketTTL is how long an untouched bucket is kept. A panel deleted, or
// simply quiet, must not hold memory forever — and a bucket reconstructed
// after this long is full anyway, so dropping it changes no decision.
const idleBucketTTL = 10 * time.Minute

// NewPushLimiter builds a limiter over the given numbers.
func NewPushLimiter(l PushLimits) *PushLimiter {
	return &PushLimiter{
		limits:     l.sane(),
		panels:     make(map[string]*bucket),
		workspaces: make(map[string]*bucket),
	}
}

// NewConfiguredPushLimiter builds a limiter over the registry's current values
// that keeps following them: an override applied in Settings reaches the next
// push, on panels already being written, without a restart.
func NewConfiguredPushLimiter() *PushLimiter {
	p := NewPushLimiter(ConfiguredPushLimits())
	p.followsRegistry = true
	return p
}

// Limits returns the numbers this limiter is enforcing — the handler needs
// MinInterval from the same instance that answered Allow, so the floor it
// applies at the write and the bucket it just consulted cannot drift.
func (p *PushLimiter) Limits() PushLimits {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limits
}

// SetLimits retunes every existing bucket in place, so an override applied in
// Settings reaches panels already being written rather than only the next new
// one.
func (p *PushLimiter) SetLimits(l PushLimits) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setLimitsLocked(l)
}

func (p *PushLimiter) setLimitsLocked(l PushLimits) {
	l = l.sane()
	if l == p.limits {
		return
	}
	p.limits = l
	for _, b := range p.panels {
		b.lim.SetLimit(perSecond(l.PanelPerMin))
		b.lim.SetBurst(l.PanelBurst)
	}
	for _, b := range p.workspaces {
		b.lim.SetLimit(perSecond(l.WorkspacePerMin))
		b.lim.SetBurst(l.WorkspacePerMin)
	}
}

// Allow reports whether one push may proceed at time now, and if not, which
// limit refused it and how long the caller must wait.
//
// Both buckets are reserved and one is cancelled if the other refuses, so a
// push rejected by the workspace backstop does not silently spend the panel's
// token too. A push that IS admitted here and then refused by the database
// floor keeps its tokens — that is deliberate: the floor only fires when a
// producer is pushing faster than it should, and charging it for the attempt is
// what makes hammering cost something.
func (p *PushLimiter) Allow(now time.Time, workspaceID, panelID string) (bool, LimitScope, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A registry-backed limiter re-reads its numbers on every push, which is
	// three map lookups, and retunes only when something actually moved. The
	// alternative is an OnChange callback threaded from the boot-time *Store
	// through two route registrations — more wiring for the same property, and
	// one more way for a limiter to be left reading a value the admin table
	// says it changed. A limiter built with explicit numbers keeps them.
	if p.followsRegistry {
		p.setLimitsLocked(ConfiguredPushLimits())
	}
	p.sweepLocked(now)

	panelBucket := p.bucketLocked(p.panels, panelID, now, perSecond(p.limits.PanelPerMin), p.limits.PanelBurst)
	wsBucket := p.bucketLocked(p.workspaces, workspaceID, now, perSecond(p.limits.WorkspacePerMin), p.limits.WorkspacePerMin)

	panelRes := panelBucket.lim.ReserveN(now, 1)
	if !panelRes.OK() {
		return false, ScopePanel, rateWindow
	}
	if d := panelRes.DelayFrom(now); d > 0 {
		panelRes.CancelAt(now)
		return false, ScopePanel, d
	}

	wsRes := wsBucket.lim.ReserveN(now, 1)
	if !wsRes.OK() {
		panelRes.CancelAt(now)
		return false, ScopeWorkspace, rateWindow
	}
	if d := wsRes.DelayFrom(now); d > 0 {
		wsRes.CancelAt(now)
		panelRes.CancelAt(now)
		return false, ScopeWorkspace, d
	}
	return true, "", 0
}

func (p *PushLimiter) bucketLocked(m map[string]*bucket, key string, now time.Time, limit rate.Limit, burst int) *bucket {
	b, ok := m[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(limit, burst)}
		m[key] = b
	}
	b.lastSeen = now
	return b
}

// sweepLocked drops buckets untouched for idleBucketTTL. Inline rather than on
// a background goroutine: this limiter is owned by a handler that tests build
// and drop freely, and a per-handler goroutine is a leak nobody remembers to
// close. Rate-limited to once per TTL so the push path stays O(1) amortised
// rather than walking every panel in the instance on every write.
func (p *PushLimiter) sweepLocked(now time.Time) {
	if now.Sub(p.lastSweep) < idleBucketTTL {
		return
	}
	p.lastSweep = now
	cutoff := now.Add(-idleBucketTTL)
	for k, b := range p.panels {
		if b.lastSeen.Before(cutoff) {
			delete(p.panels, k)
		}
	}
	for k, b := range p.workspaces {
		if b.lastSeen.Before(cutoff) {
			delete(p.workspaces, k)
		}
	}
}

func perSecond(perMin int) rate.Limit {
	return rate.Limit(float64(perMin) / rateWindow.Seconds())
}
