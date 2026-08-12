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

// EvictRing returns the entries that should be deleted, oldest first, given the
// full ring for one panel and the current time.
//
// It is a plan, not an effect: it deletes nothing and mutates nothing, so the
// caller can run it in the same transaction as the push. §10b.3 wants the floor
// enforced where the write lands rather than only in a rate limiter, because
// the rate limiter is per-process and stops holding with more than one replica.
//
// One rule is not in the PRD and is load-bearing: THE NEWEST PAYLOAD SURVIVES
// THE AGE CUT. A panel whose producer died eight days ago has nothing inside
// the window, so a plain age sweep would delete its last value — and the panel
// would flip from "stale, last value 12:40" to "never produced". Those are
// different sentences and only one of them is true. Keeping the newest row is
// what lets a long-dead panel keep saying when it died.
func EvictRing(entries []RingEntry, now time.Time) []RingEntry {
	if len(entries) == 0 {
		return nil
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

	cutoff := now.Add(-RingMaxAge)
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
