package pages

import (
	"fmt"
	"testing"
	"time"
)

// The push rate (§10b.3). The PRD's own arithmetic is why this file exists:
// 24 panels × 100 pages × one push every 5 s is 2 880 writes per second, and
// SQLite has one writer.
//
// Everything here is asserted against a supplied clock rather than by waiting.
// A rate-limit test that sleeps for its own window is a test that gets skipped.

// The numbers, read the way the handler reads them — through the registry, so
// this fails if a default is edited in ratelimitcfg without the PRD moving too.
func TestConfiguredPushLimitsMatchThePRD(t *testing.T) {
	t.Parallel()

	got := ConfiguredPushLimits()
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"push rate per panel, sustained (§10b.3)", got.PanelPerMin, 12},
		{"push burst per panel (§10b.3)", got.PanelBurst, 30},
		{"push rate per workspace (§10b.3)", got.WorkspacePerMin, 600},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// The floor is derived from the BURST, and that is the load-bearing choice.
//
// Derived from the sustained rate instead, the floor (one push per 5 s) would
// refuse the burst the bucket exists to admit — so raising the burst would
// produce MORE 429s, which is a limiter arguing with its own knob. Derived from
// the burst it is exactly as permissive as the bucket's most permissive steady
// state, and never tighter.
func TestMinIntervalIsNeverTighterThanTheBurstAllows(t *testing.T) {
	t.Parallel()

	l := ConfiguredPushLimits()
	if got, want := l.MinInterval(), 2*time.Second; got != want {
		t.Errorf("MinInterval = %s, want %s (60s ÷ burst %d)", got, want, l.PanelBurst)
	}

	// The property, not the number: a bigger burst is a looser floor.
	loose := PushLimits{PanelPerMin: 12, PanelBurst: 60, WorkspacePerMin: 600}
	if loose.MinInterval() >= l.MinInterval() {
		t.Errorf("doubling the burst made the floor tighter (%s vs %s); "+
			"an operator raising the burst must not get more 429s",
			loose.MinInterval(), l.MinInterval())
	}

	// And it is never so loose that it stops being a floor: whatever the
	// registry holds, one panel is bounded to burst writes per window against
	// any number of replicas.
	if l.MinInterval() <= 0 {
		t.Fatal("MinInterval is zero — the floor is the only limit that survives a second replica")
	}
}

// A zero or negative registry value must not wedge a panel shut forever. The
// registry's own bounds forbid it; this is the second lock on the door.
func TestPushLimitsAreClampedRatherThanTrusted(t *testing.T) {
	t.Parallel()

	broken := PushLimits{}.sane()
	if broken.PanelPerMin < 1 || broken.PanelBurst < 1 || broken.WorkspacePerMin < 1 {
		t.Fatalf("zero limits survived sanitising: %+v", broken)
	}
	if got := (PushLimits{}).MinInterval(); got <= 0 || got > rateWindow {
		t.Errorf("MinInterval on zero limits = %s; want a real interval, not a division by zero", got)
	}
}

func TestAdmitPush(t *testing.T) {
	t.Parallel()

	l := ConfiguredPushLimits()
	interval := l.MinInterval()
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	cases := []struct {
		name     string
		last     time.Time
		hasLast  bool
		wantOK   bool
		wantWait time.Duration
		why      string
	}{
		{
			name:   "a panel that has never been written",
			why:    "there is no interval to be inside of",
			wantOK: true,
		},
		{
			name:    "exactly one interval ago",
			last:    now.Add(-interval),
			hasLast: true,
			wantOK:  true,
			why:     "the interval is the limit, not the trigger — refusing here would make the sustained rate unreachable",
		},
		{
			name:     "one nanosecond inside the interval",
			last:     now.Add(-interval + time.Nanosecond),
			hasLast:  true,
			wantOK:   false,
			wantWait: time.Nanosecond,
			why:      "the boundary is tested at the boundary",
		},
		{
			name:     "two pushes at the same instant",
			last:     now,
			hasLast:  true,
			wantOK:   false,
			wantWait: interval,
			why:      "this is the N-replica case: two processes deciding at once",
		},
		{
			name:    "long after the interval",
			last:    now.Add(-time.Hour),
			hasLast: true,
			wantOK:  true,
		},
		{
			name:     "a timestamp from the future",
			last:     now.Add(time.Hour),
			hasLast:  true,
			wantOK:   false,
			wantWait: interval,
			why:      "a restored backup or a clock step must not switch the floor off; the wait is capped at one interval",
		},
	}

	for _, tc := range cases {
		ok, wait := l.AdmitPush(tc.last, tc.hasLast, now)
		if ok != tc.wantOK {
			t.Errorf("%s: admitted = %v, want %v (%s)", tc.name, ok, tc.wantOK, tc.why)
			continue
		}
		if !ok && wait != tc.wantWait {
			t.Errorf("%s: wait = %s, want %s", tc.name, wait, tc.wantWait)
		}
		if ok && wait != 0 {
			t.Errorf("%s: admitted push reported a wait of %s", tc.name, wait)
		}
	}
}

// Retry-After is an integer number of seconds, and it must never say 0 — that
// invites the immediate retry the header exists to prevent.
func TestRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want int
	}{
		{0, 1},
		{time.Nanosecond, 1},
		{time.Second, 1},
		{time.Second + time.Nanosecond, 2},
		{2 * time.Second, 2},
		{4500 * time.Millisecond, 5},
		{-time.Second, 1},
	}
	for _, tc := range cases {
		if got := RetryAfterSeconds(tc.in); got != tc.want {
			t.Errorf("RetryAfterSeconds(%s) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The bucket absorbs a burst and then settles to the sustained rate — the
// PRD's "12/min sustained, burst 30" read literally.
func TestPushLimiter_BurstThenSustained(t *testing.T) {
	t.Parallel()

	l := ConfiguredPushLimits()
	p := NewPushLimiter(l)
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	for i := 0; i < l.PanelBurst; i++ {
		if ok, scope, wait := p.Allow(now, "ws", "panel"); !ok {
			t.Fatalf("push %d of the burst refused by %s (wait %s); the burst is %d",
				i+1, scope, wait, l.PanelBurst)
		}
	}

	ok, scope, wait := p.Allow(now, "ws", "panel")
	if ok {
		t.Fatalf("push %d went through; the burst is %d and nothing has refilled", l.PanelBurst+1, l.PanelBurst)
	}
	if scope != ScopePanel {
		t.Errorf("scope = %q, want %q — the panel bucket is what ran out", scope, ScopePanel)
	}
	// One token every 60/12 s.
	if want := rateWindow / time.Duration(l.PanelPerMin); wait > want+time.Millisecond || wait < want-time.Millisecond {
		t.Errorf("wait = %s, want about %s (the sustained refill interval)", wait, want)
	}

	// A producer that waits exactly as long as it was told is admitted, which
	// is the only thing that makes Retry-After honest.
	if ok, _, _ := p.Allow(now.Add(wait), "ws", "panel"); !ok {
		t.Error("a producer that waited the full Retry-After was refused again")
	}
}

// Per-panel limits do not compose: 100 pages × 24 panels each politely under
// its own cap is still the 2 880/s scenario. The workspace bucket is the
// backstop, and it must fire on a workspace whose individual panels are all
// behaving.
func TestPushLimiter_WorkspaceBackstopCatchesWhatPanelsMiss(t *testing.T) {
	t.Parallel()

	l := ConfiguredPushLimits()
	p := NewPushLimiter(l)
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	for i := 0; i < l.WorkspacePerMin; i++ {
		// A different panel every time: no per-panel bucket comes close to its
		// own burst, so only the workspace can refuse this.
		if ok, scope, wait := p.Allow(now, "ws", fmt.Sprintf("panel-%d", i)); !ok {
			t.Fatalf("push %d refused by %s (wait %s) before the workspace cap of %d",
				i+1, scope, wait, l.WorkspacePerMin)
		}
	}

	ok, scope, wait := p.Allow(now, "ws", "panel-fresh")
	if ok {
		t.Fatalf("a workspace wrote %d payloads in one instant; the cap is %d/min",
			l.WorkspacePerMin+1, l.WorkspacePerMin)
	}
	if scope != ScopeWorkspace {
		t.Errorf("scope = %q, want %q — a fresh panel's own bucket is untouched", scope, ScopeWorkspace)
	}
	if wait <= 0 {
		t.Error("a refusal with no wait leaves the producer nothing to put in Retry-After")
	}

	// The refused push must not have spent the fresh panel's token either: a
	// producer refused by the workspace backstop should find its panel intact
	// once the workspace recovers.
	later := now.Add(time.Minute)
	for i := 0; i < l.PanelBurst; i++ {
		if ok, scope, _ := p.Allow(later, "ws", "panel-fresh"); !ok {
			t.Fatalf("push %d to the untouched panel refused by %s; the workspace-level "+
				"refusal charged the panel for a write it never made", i+1, scope)
		}
	}
}

// One panel over its limit does not throttle its neighbour, and one workspace
// does not throttle another. A shared bucket would make an unrelated page's
// producer the reason your page stopped updating.
func TestPushLimiter_BucketsAreIndependent(t *testing.T) {
	t.Parallel()

	l := ConfiguredPushLimits()
	p := NewPushLimiter(l)
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	for i := 0; i < l.PanelBurst; i++ {
		if ok, _, _ := p.Allow(now, "ws-a", "panel-hot"); !ok {
			t.Fatalf("burst push %d refused", i+1)
		}
	}
	if ok, _, _ := p.Allow(now, "ws-a", "panel-hot"); ok {
		t.Fatal("the hot panel is not actually limited")
	}
	if ok, scope, _ := p.Allow(now, "ws-a", "panel-quiet"); !ok {
		t.Errorf("a quiet panel in the same workspace was refused by %s", scope)
	}
	// A different workspace, and therefore a different panel: the panel key is
	// the panel's row id, which is a primary key and so already globally
	// unique — two workspaces cannot name the same bucket.
	if ok, scope, _ := p.Allow(now, "ws-b", "panel-elsewhere"); !ok {
		t.Errorf("a panel in a different workspace was refused by %s", scope)
	}
}

// SetLimits is the live-apply hook: an override typed into Settings has to
// reach panels already being written, not only the next new one.
func TestPushLimiter_SetLimitsRetunesExistingBuckets(t *testing.T) {
	t.Parallel()

	p := NewPushLimiter(PushLimits{PanelPerMin: 12, PanelBurst: 2, WorkspacePerMin: 600})
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	for i := 0; i < 2; i++ {
		if ok, _, _ := p.Allow(now, "ws", "panel"); !ok {
			t.Fatalf("push %d refused under a burst of 2", i+1)
		}
	}
	if ok, _, _ := p.Allow(now, "ws", "panel"); ok {
		t.Fatal("the third push went through under a burst of 2")
	}

	p.SetLimits(PushLimits{PanelPerMin: 12, PanelBurst: 30, WorkspacePerMin: 600})
	if got := p.Limits().MinInterval(); got != 2*time.Second {
		t.Errorf("MinInterval after retuning = %s, want 2s — the floor follows the burst", got)
	}
	if ok, scope, wait := p.Allow(now, "ws", "panel"); !ok {
		t.Errorf("the existing bucket kept the old burst after SetLimits (%s, wait %s); "+
			"an override that only reaches new panels is an override nobody can see working", scope, wait)
	}
}

// The server's limiter tracks the admin table rather than a snapshot taken at
// boot: a value typed into Settings has to reach the next push. A limiter built
// with explicit numbers is the opposite contract and keeps what it was given —
// that is what makes the test above mean anything.
func TestPushLimiter_ConfiguredLimiterFollowsTheRegistry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	following := NewConfiguredPushLimiter()
	following.SetLimits(PushLimits{PanelPerMin: 1, PanelBurst: 1, WorkspacePerMin: 1})
	following.Allow(now, "ws", "panel")
	if got, want := following.Limits(), ConfiguredPushLimits(); got != want {
		t.Errorf("registry-backed limiter kept %+v after a push, want the registry's %+v", got, want)
	}

	pinned := NewPushLimiter(PushLimits{PanelPerMin: 1, PanelBurst: 1, WorkspacePerMin: 1})
	pinned.Allow(now, "ws", "panel")
	if got := pinned.Limits(); got.PanelBurst != 1 {
		t.Errorf("a limiter built with explicit numbers drifted to %+v", got)
	}
}

// Buckets for panels nobody writes any more must not accumulate. A bucket
// rebuilt after the TTL is full, so dropping it changes no decision.
func TestPushLimiter_ForgetsIdleBuckets(t *testing.T) {
	t.Parallel()

	p := NewPushLimiter(ConfiguredPushLimits())
	now := time.Date(2026, 8, 12, 9, 14, 22, 0, time.UTC)

	for i := 0; i < 50; i++ {
		p.Allow(now, "ws", fmt.Sprintf("panel-%d", i))
	}
	p.Allow(now.Add(2*idleBucketTTL), "ws", "panel-still-here")

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.panels) != 1 {
		t.Errorf("panel buckets after the TTL = %d, want 1 (only the one just written)", len(p.panels))
	}
}
