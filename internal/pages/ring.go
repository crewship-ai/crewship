package pages

import (
	"sort"
	"time"
)

// The payload ring (PRD §5, §10b.3).
//
// Pages does not answer "show me last March" — history, alerting and
// correlation live in the journal, in automations and in the causal chain, not
// in the panel. What the panel keeps is a bounded ring of previous payloads:
// enough for a sparkline and for "what did this look like before it broke",
// deliberately not enough to be a time-series database.
//
// Bounded by COUNT FIRST and AGE SECOND. "About a week" is right for the age
// bound but cannot be the only bound: a panel pushed every 5 s produces
// ~120 000 rows in a week, per panel.

// RingEntry is one stored payload, reduced to what eviction needs.
type RingEntry struct {
	Seq        int64
	ProducedAt time.Time
}

// DefaultPageRetentionDays is the age cut applied when a workspace records no
// opinion — workspaces.page_retention_days IS NULL, which is the value every
// existing row has and the value a fresh one gets. Derived from RingMaxAge so
// the two can never disagree.
//
// The column follows the run_retention_days convention exactly
// (migrate_consts_v158_run_retention_days.go:13): a nullable INTEGER on
// workspaces, with NULL — and, as there, a non-positive value — meaning "use
// the instance default". Nullable rather than NOT NULL DEFAULT 7 because the
// default has to be movable later without rewriting every row.
const DefaultPageRetentionDays = int(RingMaxAge / (24 * time.Hour))

// RetentionAge turns a workspace's page_retention_days into the age cut the
// ring applies. days <= 0 (which includes a NULL column read as zero) means the
// instance default.
//
// Note what is NOT configurable: RingMaxPayloads. Count is the bound that
// makes the storage predictable — a panel pushed every 2 s produces ~300 000
// rows a week — so a workspace can lengthen or shorten its window but cannot
// turn the ring into a time-series database.
func RetentionAge(days int) time.Duration {
	if days <= 0 {
		return RingMaxAge
	}
	return time.Duration(days) * 24 * time.Hour
}

// EvictRing returns the entries that should be deleted, oldest first, given the
// full ring for one panel and the current time.
//
// It is a plan, not an effect: it deletes nothing and mutates nothing, so the
// caller can run it in the same transaction as the push — the ring is bounded
// by the write that grows it rather than by a sweep that might not run. (The
// other thing §10b.3 wants in that transaction is the push-rate floor, which is
// a different rule and lives in limits.go.)
//
// One rule is not in the PRD and is load-bearing: THE NEWEST PAYLOAD SURVIVES
// THE AGE CUT. A panel whose producer died eight days ago has nothing inside
// the window, so a plain age sweep would delete its last value — and the panel
// would flip from "stale, last value 12:40" to "never produced". Those are
// different sentences and only one of them is true. Keeping the newest row is
// what lets a long-dead panel keep saying when it died.
func EvictRing(entries []RingEntry, now time.Time) []RingEntry {
	return EvictRingWithin(entries, now, RingMaxAge)
}

// EvictRingWithin is EvictRing with the age cut supplied rather than assumed —
// the workspace's page_retention_days (RetentionAge). The 7-day figure is the
// DEFAULT, not a property of the storage, so a workspace that wants three days
// or thirty says so in a column instead of asking for a release.
//
// maxAge <= 0 falls back to the default rather than disabling the age cut. "No
// opinion" and "keep forever" are different sentences, and only the first one
// is expressible in a nullable integer that defaults to NULL.
func EvictRingWithin(entries []RingEntry, now time.Time, maxAge time.Duration) []RingEntry {
	if len(entries) == 0 {
		return nil
	}
	if maxAge <= 0 {
		maxAge = RingMaxAge
	}

	// Work on a copy: the caller's slice is usually the rows it just read and
	// is about to write back.
	ordered := make([]RingEntry, len(entries))
	copy(ordered, entries)
	// Newest first. Seq breaks ties, since two pushes can land inside one
	// timestamp's resolution and the ring's order has to be total.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ProducedAt.Equal(ordered[j].ProducedAt) {
			return ordered[i].Seq > ordered[j].Seq
		}
		return ordered[i].ProducedAt.After(ordered[j].ProducedAt)
	})

	cutoff := now.Add(-maxAge)
	var evicted []RingEntry
	for i, e := range ordered {
		if i == 0 {
			// The last known value. It is what the panel renders while it is
			// stale, so it outlives both bounds.
			continue
		}
		if i >= RingMaxPayloads || !e.ProducedAt.After(cutoff) {
			evicted = append(evicted, e)
		}
	}

	// Oldest first, so a caller deleting in order never leaves the ring in a
	// state where the newest rows are gone and the oldest are not.
	sort.SliceStable(evicted, func(i, j int) bool {
		if evicted[i].ProducedAt.Equal(evicted[j].ProducedAt) {
			return evicted[i].Seq < evicted[j].Seq
		}
		return evicted[i].ProducedAt.Before(evicted[j].ProducedAt)
	})
	return evicted
}

// EvictRing is the same rule read against the evaluator's injected clock, so
// the sweep is scheduled and tested the same way the freshness states are.
func (e *Evaluator) EvictRing(entries []RingEntry) []RingEntry {
	return EvictRing(entries, e.clock.Now())
}

// EvictRingWithin is the retention-aware form against the same clock.
func (e *Evaluator) EvictRingWithin(entries []RingEntry, maxAge time.Duration) []RingEntry {
	return EvictRingWithin(entries, e.clock.Now(), maxAge)
}
