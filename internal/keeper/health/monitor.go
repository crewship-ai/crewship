// Package health measures the Keeper's own decision-making and raises an
// alarm when the shape of it stops making sense.
//
// Keeper had NO metric on its own verdicts. #1624 made it deny every single
// credential request and nothing noticed for several milestones — not a test,
// not the admin page, not an operator — because a fail-closed security layer
// that denies everything is indistinguishable from a working one unless you
// are the agent being denied. Every other defence in Keeper (tier floors,
// intent length, injection markers, fail-closed parsing) is aimed at a failure
// somebody thought of in advance. This one watches the OUTPUT DISTRIBUTION, so
// it also catches the failures nobody has thought of yet. That is why it is a
// separate concern rather than one more check inside the gatekeeper.
//
// The shape is copied from internal/harbormaster/reward.go, which already
// keeps a rolling window of its own outcomes and reacts when the deny rate
// crosses AutoUpgradeDenyRate. Two deliberate differences:
//
//   - The window lives in memory, not in a table. Harbormaster's history feeds
//     a DECISION (auto-tuning a gate mode) and has to survive a restart; this
//     one feeds an ALARM. A restart clears the window, which delays an alarm by
//     MinSamples decisions and can never invent a false one — an acceptable
//     trade for not needing a schema migration to start watching.
//   - Recording happens on the credential hot path. Record does O(1) work under
//     a short mutex, returns no error, and writes nothing to disk. See Record.
package health

import (
	"sort"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

const (
	// DefaultWindowSize is how many recent verdicts per workspace are kept.
	// Large enough that a genuine run of denials (an agent probing a fence it
	// keeps hitting) does not by itself look like an outage, small enough that
	// a fixed instance stops alarming within a day of normal traffic rather
	// than carrying its bad history forever.
	DefaultWindowSize = 200

	// MinSamples is the quorum before any alarm can fire. Without it, the
	// first four requests on a fresh instance — all legitimately denied —
	// would page somebody, and an alarm that cries wolf on day one is an
	// alarm operators mute before the day it matters. 20 consecutive
	// non-ALLOW verdicts is far outside the variance of ordinary traffic;
	// four is not evidence of anything.
	MinSamples = 20

	// AlarmAllowRate is the floor the PROGRESSED share must stay above —
	// verdicts that granted access OR routed it to a human. Mirrors
	// harbormaster's AutoUpgradeDenyRate in spirit: a single number over a
	// rolling window that says "this is no longer normal". 0.05 means "at
	// least one request in twenty got somewhere" — #1624 sat at exactly 0.00.
	//
	// It is deliberately NOT the ALLOW share. The tier policy converts every
	// ALLOW into an ESCALATE at a HumanApproval tier (internal/keeper/tier.go),
	// and L4 sets that flag — so a workspace whose credentials are all L4 runs
	// at an ALLOW rate of exactly zero while working perfectly. Alarming there
	// would re-fire every cooldown forever, and an alarm that cries wolf gets
	// muted before the day it matters. An escalation is the Keeper doing its
	// job; a refusal is the only thing that looks like the outage.
	AlarmAllowRate = 0.05

	// AlarmJudgeFailureRate is the share of verdicts the judge itself could
	// not supply — unreachable, timed out, or unparseable. Each one is a DENY
	// the gatekeeper wrote on the model's behalf. A quarter of all decisions
	// coming from the fallback rather than the judge means Keeper is running
	// blind even if its allow rate still looks survivable.
	AlarmJudgeFailureRate = 0.25

	// AlarmCooldown is the minimum gap between two alarms of the same kind in
	// one workspace. The alarm condition stays true for as long as the window
	// holds bad samples, so without this every subsequent credential request
	// would mint another inbox card for the same outage.
	AlarmCooldown = 6 * time.Hour

	// MaxWorkspaces bounds the number of tracked windows so a long-lived
	// server with many workspaces cannot grow this metric without limit. When
	// the cap is hit the least-recently-recorded workspace is dropped: it is
	// the one whose Keeper is least active, so it is the one whose alarm is
	// least urgent. Metrics degrade; decisions never do.
	MaxWorkspaces = 64
)

// Verdict is one Keeper decision as the hot path saw it. It is a value, not a
// pointer, so Record cannot retain anything the caller still owns.
type Verdict struct {
	WorkspaceID string
	// Decision is the FINAL verdict handed back to the agent (after the tier
	// floor), as a keeper.Decision string. Anything unrecognised counts as
	// Other, never as an ALLOW — see Stats.Other.
	Decision string
	// JudgeFailed is keeper.GatekeeperResponse.InfraFailure: this DENY was
	// produced by the gatekeeper itself because the judge was unreachable,
	// timed out, or returned something unparseable. The PRD calls this the
	// "unparseable-response rate"; the producer's flag is deliberately wider
	// because all three sub-cases mean the same thing here — the verdict did
	// not come from a model that read the request.
	JudgeFailed bool
	// Latency is how long the judge took to answer.
	Latency time.Duration
	// At is when the verdict happened. Zero means now. Set explicitly by tests
	// and by any caller replaying history; the cooldown and the window both
	// key off it.
	At time.Time
}

// Stats is the rolling tally for one workspace, in the same spirit as
// harbormaster's OutcomeCounts: plain counts plus rate helpers, so the
// thresholds above read as prose at the call site.
type Stats struct {
	WorkspaceID string
	Samples     int
	Allow       int
	Deny        int
	Escalate    int
	// Other counts verdicts that are none of the three. NormalizeRawResponse
	// is supposed to make this impossible, which is exactly why it is counted
	// separately instead of being folded into Deny — this metric exists for
	// the failures nobody predicted, and silently reclassifying an unknown
	// verdict would hide one.
	Other int
	// JudgeFailures counts verdicts the gatekeeper wrote because the judge
	// could not answer.
	JudgeFailures int
	P95Latency    time.Duration
	Oldest        time.Time
	Newest        time.Time
}

// AllowRate is the share of verdicts that granted access. Every recorded
// verdict is in the denominator — unlike harbormaster's ApproveRate, which
// excludes timeouts as non-signal, there is no such thing as a Keeper verdict
// that was not handed to an agent.
//
// Reported, but NOT what the collapse alarm reads: see ProgressedRate.
func (s Stats) AllowRate() float64 { return share(s.Allow, s.Samples) }

// ProgressedRate is the share of verdicts that did not refuse — granted, or
// sent to a human. It is the health signal, because those are the two outcomes
// that leave the requester somewhere other than a dead end, and an L4-only
// workspace legitimately produces nothing but the second.
func (s Stats) ProgressedRate() float64 { return share(s.Allow+s.Escalate, s.Samples) }

// DenyRate is the share of verdicts that refused access.
func (s Stats) DenyRate() float64 { return share(s.Deny, s.Samples) }

// EscalateRate is the share of verdicts that asked a human.
func (s Stats) EscalateRate() float64 { return share(s.Escalate, s.Samples) }

// JudgeFailureRate is the share of verdicts the judge did not actually
// produce.
func (s Stats) JudgeFailureRate() float64 { return share(s.JudgeFailures, s.Samples) }

func share(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// AlarmKind names which invariant broke. It is part of the inbox item's
// dedup key, so these strings are stable.
type AlarmKind string

const (
	// AlarmAllowCollapse: Keeper is refusing effectively everything. The
	// #1624 signature.
	AlarmAllowCollapse AlarmKind = "allow_rate_collapsed"
	// AlarmJudgeFailures: the judge is not supplying verdicts and the
	// fail-closed fallback is deciding instead.
	AlarmJudgeFailures AlarmKind = "judge_failure_spike"
)

// Alarm is a raised condition plus the numbers that raised it, so the inbox
// card can show an operator what to look at rather than just that something
// is wrong.
type Alarm struct {
	Kind        AlarmKind
	WorkspaceID string
	Summary     string
	Stats       Stats
	At          time.Time
}

// Alarm evaluates the thresholds against a snapshot and reports whether one
// tripped. Pure and exported so the condition can be checked from a read path
// (an admin page, a CLI status command) without recording anything.
//
// When both conditions hold the judge-failure alarm wins. An unusable judge
// denies everything by construction, so "the allow rate is zero" is its
// symptom; reporting the symptom would send an operator to review the policy
// when the model is what is broken.
func (s Stats) Alarm() (Alarm, bool) {
	if s.Samples < MinSamples {
		return Alarm{}, false
	}
	a := Alarm{WorkspaceID: s.WorkspaceID, Stats: s, At: s.Newest}
	switch {
	case s.JudgeFailureRate() >= AlarmJudgeFailureRate:
		a.Kind = AlarmJudgeFailures
		a.Summary = summaryJudgeFailures(s)
	case s.ProgressedRate() < AlarmAllowRate:
		a.Kind = AlarmAllowCollapse
		a.Summary = summaryAllowCollapse(s)
	default:
		return Alarm{}, false
	}
	return a, true
}

// sample is one slot in the ring. Kept to three fields so a window of 200 is
// under 5 KB per workspace.
type sample struct {
	decision    string
	judgeFailed bool
	latency     time.Duration
	at          time.Time
}

// window is a fixed-size ring with running counters, so Record is O(1) and
// only Snapshot pays for the percentile sort.
type window struct {
	buf    []sample
	next   int
	filled bool

	allow, deny, escalate, other, judgeFailures int

	lastRecord time.Time
	lastAlarm  map[AlarmKind]time.Time
}

// Monitor holds one rolling window per workspace.
//
// A single mutex covers every workspace. Keeper decisions are seconds apart
// per agent and gated behind an LLM call; contention here is not measurable,
// and one lock is far easier to reason about than per-workspace locking plus
// a map lock.
type Monitor struct {
	mu      sync.Mutex
	size    int
	windows map[string]*window
}

// NewMonitor returns a Monitor keeping size verdicts per workspace. A
// non-positive size falls back to DefaultWindowSize.
func NewMonitor(size int) *Monitor {
	if size <= 0 {
		size = DefaultWindowSize
	}
	return &Monitor{size: size, windows: make(map[string]*window)}
}

// Default is the process-wide monitor the API handlers record into. A package
// var rather than a field threaded through the handlers so wiring the metric
// costs exactly one call at each decision site — mirrors how
// inbox.SetExternalNotifier keeps its fan-out out of every caller's signature.
var Default = NewMonitor(DefaultWindowSize)

// Record adds a verdict to the workspace's window and reports the alarm it
// raised, if any.
//
// Contract, because this runs while an agent is blocked waiting on a
// credential decision: it never blocks on I/O, never returns an error, and
// never panics. A verdict missing its workspace or decision is dropped rather
// than counted — a caller bug must not become a fake sample that moves a
// threshold. Same "caller bugs are a silent no-op" discipline as
// inbox.Insert's envelope validation.
func (m *Monitor) Record(v Verdict) (Alarm, bool) {
	if m == nil || v.WorkspaceID == "" || v.Decision == "" {
		return Alarm{}, false
	}
	at := v.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	w := m.windows[v.WorkspaceID]
	if w == nil {
		m.evictIfFullLocked()
		w = &window{buf: make([]sample, m.size), lastAlarm: make(map[AlarmKind]time.Time)}
		m.windows[v.WorkspaceID] = w
	}
	w.add(sample{decision: v.Decision, judgeFailed: v.JudgeFailed, latency: v.Latency, at: at})

	s := w.stats(v.WorkspaceID)
	a, ok := s.Alarm()
	if !ok {
		return Alarm{}, false
	}
	if last, seen := w.lastAlarm[a.Kind]; seen && at.Sub(last) < AlarmCooldown {
		return Alarm{}, false
	}
	w.lastAlarm[a.Kind] = at
	a.At = at
	return a, true
}

// Snapshot returns the current tally for a workspace. The bool is false when
// nothing has been recorded — distinct from an all-zero Stats, which would
// otherwise read as "this workspace has never allowed anything".
func (m *Monitor) Snapshot(workspaceID string) (Stats, bool) {
	if m == nil || workspaceID == "" {
		return Stats{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.windows[workspaceID]
	if w == nil {
		return Stats{}, false
	}
	return w.stats(workspaceID), true
}

// TrackedWorkspaces reports how many windows are live. Exists so the bound
// from MaxWorkspaces is assertable rather than a comment.
// Reset drops every window. For tests, and for a caller that genuinely wants
// the rolling picture to start over — the windows are in memory, so a restart
// does this anyway.
func (m *Monitor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.windows = make(map[string]*window, len(m.windows))
}

func (m *Monitor) TrackedWorkspaces() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.windows)
}

// evictIfFullLocked drops the least-recently-recorded window when the cap is
// reached. Linear over MaxWorkspaces entries and only on the first verdict of
// a workspace we are not yet tracking, so it never runs on a warm path.
func (m *Monitor) evictIfFullLocked() {
	if len(m.windows) < MaxWorkspaces {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, w := range m.windows {
		if oldestID == "" || w.lastRecord.Before(oldest) {
			oldestID, oldest = id, w.lastRecord
		}
	}
	delete(m.windows, oldestID)
}

// add writes one sample, decrementing the counters of whatever it overwrote so
// the tallies stay exact without a rescan.
func (w *window) add(s sample) {
	if w.filled {
		w.countDecision(w.buf[w.next], -1)
	}
	w.buf[w.next] = s
	w.countDecision(s, +1)
	w.next++
	if w.next == len(w.buf) {
		w.next = 0
		w.filled = true
	}
	w.lastRecord = s.at
}

func (w *window) countDecision(s sample, delta int) {
	switch keeper.Decision(s.decision) {
	case keeper.DecisionAllow:
		w.allow += delta
	case keeper.DecisionDeny:
		w.deny += delta
	case keeper.DecisionEscalate:
		w.escalate += delta
	default:
		w.other += delta
	}
	if s.judgeFailed {
		w.judgeFailures += delta
	}
}

func (w *window) len() int {
	if w.filled {
		return len(w.buf)
	}
	return w.next
}

func (w *window) stats(workspaceID string) Stats {
	n := w.len()
	s := Stats{
		WorkspaceID:   workspaceID,
		Samples:       n,
		Allow:         w.allow,
		Deny:          w.deny,
		Escalate:      w.escalate,
		Other:         w.other,
		JudgeFailures: w.judgeFailures,
	}
	if n == 0 {
		return s
	}
	lat := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		e := w.buf[i]
		lat = append(lat, e.latency)
		if s.Oldest.IsZero() || e.at.Before(s.Oldest) {
			s.Oldest = e.at
		}
		if e.at.After(s.Newest) {
			s.Newest = e.at
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	// Nearest rank (ceil(0.95n)-th smallest), done in integer arithmetic so a
	// float rounding wobble at exactly 0.95n cannot shift the index by one. A
	// single slow outlier in a window of 20 must not become "p95", or the
	// number reports the worst case and stops describing the typical one.
	idx := (95*n+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	s.P95Latency = lat[idx]
	return s
}
