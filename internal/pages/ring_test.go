package pages

import (
	"testing"
	"time"
)

// The payload ring (§5, §10b.3). A bounded number of previous payloads per
// panel — enough for a sparkline and for "what did this look like before it
// broke", deliberately not enough to be a time-series database.
//
// It is bounded by COUNT FIRST and AGE SECOND, and the PRD is specific about
// why: "about a week" is right for the age bound but cannot be the only bound,
// because a panel pushed every 5 s produces ~120 000 rows in a week, per panel.

// ring builds n entries ending at `newest`, one `step` apart, numbered from
// startSeq with seq ascending in step with time — which is what the writer
// guarantees and what the eviction relies on.
func ring(startSeq int64, newest time.Time, n int, step time.Duration) []RingEntry {
	out := make([]RingEntry, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, RingEntry{
			Seq:        startSeq + int64(n-1-i),
			ProducedAt: newest.Add(-time.Duration(i) * step),
		})
	}
	return out
}

func seqs(entries []RingEntry) []int64 {
	out := make([]int64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Seq)
	}
	return out
}

func TestEvictRing_CountAndAgeCuts(t *testing.T) {
	t.Parallel()

	now := epoch

	cases := []struct {
		name        string
		entries     []RingEntry
		wantEvicted []int64
		why         string
	}{
		{
			name:    "an empty ring",
			entries: nil,
			why:     "nothing to evict and nothing to panic about",
		},
		{
			name:    "exactly at the count limit",
			entries: ring(1, now, RingMaxPayloads, time.Minute),
			why:     "200 is the limit, not the trigger — evicting at N would keep 199",
		},
		{
			name:        "one over the count limit",
			entries:     ring(1, now, RingMaxPayloads+1, time.Minute),
			wantEvicted: []int64{1},
			why:         "the oldest payload leaves, and only the oldest",
		},
		{
			name:        "far over the count limit",
			entries:     ring(1, now, RingMaxPayloads+5, time.Minute),
			wantEvicted: []int64{1, 2, 3, 4, 5},
			why:         "a burst producer catching up must not leave the ring permanently over budget",
		},
		{
			name: "the age cut fires before the count does",
			entries: []RingEntry{
				{Seq: 1, ProducedAt: now.Add(-8 * 24 * time.Hour)},
				{Seq: 2, ProducedAt: now.Add(-7*24*time.Hour - time.Second)},
				{Seq: 3, ProducedAt: now.Add(-24 * time.Hour)},
				{Seq: 4, ProducedAt: now.Add(-time.Minute)},
			},
			wantEvicted: []int64{1, 2},
			why:         "four payloads is well inside 200, so only the 7-day cut can be doing this (§10b.3)",
		},
		{
			name: "exactly at the age boundary",
			entries: []RingEntry{
				{Seq: 1, ProducedAt: now.Add(-RingMaxAge)},
				{Seq: 2, ProducedAt: now.Add(-RingMaxAge + time.Nanosecond)},
				{Seq: 3, ProducedAt: now},
			},
			wantEvicted: []int64{1},
			why:         "a payload exactly 7 days old is outside a 7-day window, the same way an SLA deadline is",
		},
		{
			name: "both cuts at once",
			entries: append(ring(1, now.Add(-8*24*time.Hour), 3, time.Minute),
				ring(4, now, RingMaxPayloads+2, time.Minute)...),
			wantEvicted: []int64{1, 2, 3, 4, 5},
			why:         "whichever comes first means both are applied, not the stricter one chosen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seqs(EvictRing(tc.entries, now))
			if len(got) != len(tc.wantEvicted) {
				t.Fatalf("evicted %v, want %v — %s", got, tc.wantEvicted, tc.why)
			}
			for i := range got {
				if got[i] != tc.wantEvicted[i] {
					t.Fatalf("evicted %v, want %v — %s", got, tc.wantEvicted, tc.why)
				}
			}
		})
	}
}

// The trap in a pure age cut: a panel whose producer died eight days ago has
// nothing left inside the window, so a naive sweep deletes its last payload —
// and the panel flips from "stale, last value 12:40" (§9b.4 row 2) to "never
// produced" (row 4). Those are different sentences and only one of them is
// true. The newest payload therefore survives every age cut.
func TestEvictRing_TheLastKnownValueOutlivesTheAgeCut(t *testing.T) {
	t.Parallel()

	now := epoch
	entries := []RingEntry{
		{Seq: 1, ProducedAt: now.Add(-30 * 24 * time.Hour)},
		{Seq: 2, ProducedAt: now.Add(-20 * 24 * time.Hour)},
		{Seq: 3, ProducedAt: now.Add(-10 * 24 * time.Hour)},
	}

	got := seqs(EvictRing(entries, now))
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("evicted %v, want [1 2]: everything outside the window goes EXCEPT the newest payload, "+
			"which is what lets the panel keep saying 'stale since <date>' instead of 'never produced'", got)
	}
}

// The age cut is a DEFAULT, not a property of the storage: §10b.3 makes it
// configurable per workspace as page_retention_days, following
// run_retention_days. NULL — which is what every existing workspace has — means
// the instance default, and so does any non-positive value.
func TestRetentionAge_NullAndNonsenseMeanTheInstanceDefault(t *testing.T) {
	t.Parallel()

	if got := RetentionAge(0); got != RingMaxAge {
		t.Errorf("RetentionAge(0) = %s, want the default %s — a NULL column reads as zero", got, RingMaxAge)
	}
	if got := RetentionAge(-3); got != RingMaxAge {
		t.Errorf("RetentionAge(-3) = %s, want the default %s", got, RingMaxAge)
	}
	if got, want := RetentionAge(3), 3*24*time.Hour; got != want {
		t.Errorf("RetentionAge(3) = %s, want %s", got, want)
	}
	if DefaultPageRetentionDays != 7 {
		t.Errorf("DefaultPageRetentionDays = %d, want 7 (§10b.3's hard age cut)", DefaultPageRetentionDays)
	}
	if RetentionAge(DefaultPageRetentionDays) != RingMaxAge {
		t.Error("the default in days and RingMaxAge disagree; they are the same bound written twice")
	}
}

// A workspace with a shorter window gets a shorter window, and the ring's other
// rules do not change with it: the count bound still applies, and the newest
// payload still survives. That last one is what stops a three-day window from
// turning a producer that died last week into "never produced".
func TestEvictRingWithin_HonoursAPerWorkspaceWindow(t *testing.T) {
	t.Parallel()

	now := epoch
	entries := []RingEntry{
		{Seq: 1, ProducedAt: now.Add(-6 * 24 * time.Hour)},
		{Seq: 2, ProducedAt: now.Add(-2 * 24 * time.Hour)},
		{Seq: 3, ProducedAt: now.Add(-time.Hour)},
	}

	// The instance default keeps all three: six days is inside seven.
	if got := seqs(EvictRingWithin(entries, now, RetentionAge(0))); len(got) != 0 {
		t.Fatalf("evicted %v under the default window; all three payloads are inside 7 days", got)
	}

	// Three days evicts the six-day-old one and nothing else.
	got := seqs(EvictRingWithin(entries, now, RetentionAge(3)))
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("evicted %v under a 3-day window, want [1]", got)
	}

	// A window of one day, with every payload outside it: the newest still
	// survives (§9b.4 row 2 vs row 4).
	got = seqs(EvictRingWithin(entries, now, RetentionAge(1)))
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("evicted %v under a 1-day window, want [1 2] — the last known value outlives every age cut", got)
	}

	// A longer window than the default is equally a workspace's business.
	if got := seqs(EvictRingWithin(entries, now, RetentionAge(30))); len(got) != 0 {
		t.Fatalf("evicted %v under a 30-day window", got)
	}

	// And the count bound is NOT configurable: it still fires inside any window.
	dense := ring(1, now, RingMaxPayloads+1, time.Second)
	if got := seqs(EvictRingWithin(dense, now, RetentionAge(30))); len(got) != 1 || got[0] != 1 {
		t.Fatalf("evicted %v with 201 payloads inside a 30-day window, want [1]: "+
			"a longer window must not turn the ring into a time-series database", got)
	}
}

// The eviction is a plan, not an effect: it returns the rows to delete and
// touches nothing. That is what lets the caller run it inside the same
// transaction as the push, which is where §10b.3 wants the floor enforced —
// "regardless of how many processes are serving".
func TestEvictRing_DoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	entries := ring(1, epoch, RingMaxPayloads+3, time.Minute)
	before := seqs(entries)

	EvictRing(entries, epoch)

	after := seqs(entries)
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("EvictRing reordered its input: %v became %v", before, after)
		}
	}
}

// The evaluator carries the clock, so the sweep can be scheduled and tested the
// same way the freshness states are.
func TestEvaluator_EvictRingUsesTheInjectedClock(t *testing.T) {
	t.Parallel()

	entries := []RingEntry{
		{Seq: 1, ProducedAt: epoch.Add(-6 * 24 * time.Hour)},
		{Seq: 2, ProducedAt: epoch},
	}

	clock := &fixedClock{now: epoch}
	if got := NewEvaluator(clock).EvictRing(entries); len(got) != 0 {
		t.Fatalf("evicted %v at t0; both payloads are inside the 7-day window", seqs(got))
	}

	clock.Advance(2 * 24 * time.Hour)
	got := NewEvaluator(clock).EvictRing(entries)
	if len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("evicted %v two days later, want [1] — the older payload has crossed the age cut", seqs(got))
	}
}

// The numbers themselves. They are structural bounds rather than rate limits:
// §10b.3 puts the PUSH RATE in config/rate-limits.yml so it is reviewable and
// adjustable in Settings, and keeps the ring depth and the size caps as
// properties of the storage shape.
func TestRingAndPayloadLimitsMatchThePRD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  int64
		want int64
	}{
		{"payload ring depth (§10b.3)", RingMaxPayloads, 200},
		{"payload ring age in hours (§10b.3)", int64(RingMaxAge / time.Hour), 7 * 24},
		{"payload size cap in KiB (§10)", MaxPayloadBytes / 1024, 64},
		{"spec size cap in KiB (§10)", MaxSpecBytes / 1024, 256},
		{"panels per page (§10b.3)", MaxPanelsPerPage, 24},
		{"pages per workspace (§10b.3)", MaxPagesPerWorkspace, 100},
		{"versions per page (§10b.1)", MaxVersionsPerPage, 50},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
