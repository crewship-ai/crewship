package chain

import (
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ---------------------------------------------------------------------------
// When a node happened, and how long it took.
//
// Two problems, kept apart on purpose.
//
// The first is that the timestamp columns this walk reads hold THREE syntaxes,
// because more than one writer stamps each of them:
//
//   - the fixed-width tsformat form "2026-08-07T09:41:02.500000000Z"
//     (pipeline.RunStore, internal/inbox);
//   - plain RFC3339 at second precision "2026-08-07T09:41:02Z"
//     (internal/api/assignments_run.go, internal/orchestrator);
//   - SQLite's own "2026-08-07 09:41:02.317" — a space instead of the T, no
//     zone at all — written by every `datetime('now','subsec')` DEFAULT and by
//     the crew-slot CAS in internal/api/assignments_queue.go.
//
// A reader that knows only RFC3339 silently drops the third, and the third is
// not an edge case: it is every assignment that ever contended for a crew slot.
//
// The second problem is what a client does with the string afterwards. The
// space form is not ISO 8601, so `new Date(s)` reads it as LOCAL time in V8 and
// returns Invalid Date in stricter engines — a node lands hours from where it
// belongs, or nowhere, and neither failure announces itself. So instants are
// parsed here and re-emitted in ONE form, the same fixed-width UTC form
// tsformat writes.
//
// Unparseable stays absent rather than passing through raw. A raw string a
// client cannot parse is not more information than no string; it is the same
// absence with a rendering bug attached.
// ---------------------------------------------------------------------------

// instantLayouts are the shapes above, most specific first. time.Parse tolerates
// a fractional part the layout does not name, so the two SQLite entries between
// them cover both `datetime('now')` and `datetime('now','subsec')`; both are
// spelled anyway, so a future switch to a stricter parser cannot quietly drop
// one. Mirrors parseRunTime in internal/api/issue_handler_runs.go, which solved
// the same problem for the run→files window (#891).
var instantLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// parseInstant reads any of the stored syntaxes. A value SQLite wrote without a
// zone is UTC by construction (`datetime('now', …)` is always UTC), which is
// what time.Parse assumes for a layout naming no zone, so the two agree.
func parseInstant(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			// A zero time is what a Go writer produces from an unset
			// time.Time, and it renders as year 1 / 1970 depending on who
			// reads it. It sorts to the top of every timeline and means
			// nothing, so it is treated as the absence it actually is.
			if t.IsZero() {
				return time.Time{}, false
			}
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// normaliseInstant returns the one wire form, or "" when the column held
// nothing this package can place on an axis.
func normaliseInstant(raw string) string {
	t, ok := parseInstant(raw)
	if !ok {
		return ""
	}
	return tsformat.Format(t)
}

// spanMS is the wall clock between two stored stamps, in milliseconds, or nil
// when there is no span to measure.
//
// Wall clock rather than a stored duration column, deliberately. pipeline_runs
// has duration_ms and it is the wrong number twice over: it is NOT NULL DEFAULT
// 0 and rewritten at every step boundary, so an in-flight run carries a partial
// value that reads as a finished one; and a routine of agentless steps records
// 0 on every run, so work that took three minutes reports "instant". The same
// reasoning settled chainElapsedMS in internal/api/chains_list.go, one level up.
//
// nil, not 0, whenever the span is underivable — no end yet, an unparseable
// stamp, or an end before its start. 0 asserts "it was instant", which is a
// different claim from "we cannot say".
//
// A genuine 0 IS returned when both stamps parse and the interval rounds under
// a millisecond, because at node level an end that exists is unambiguous: the
// thing finished, very fast. That is why the result is a pointer — 0 and absent
// are different answers and must survive JSON as different answers.
func spanMS(start, end string) *int64 {
	s, sok := parseInstant(start)
	e, eok := parseInstant(end)
	if !sok || !eok {
		return nil
	}
	ms := e.Sub(s).Milliseconds()
	if ms < 0 {
		return nil
	}
	return &ms
}

// withSpan stamps the three timing fields onto a node from a (start, end) pair
// of stored strings.
//
// The end is withheld whenever the beginning is missing, and that is the rule
// worth being explicit about. assignments can hold finished_at with started_at
// still NULL — MissionEngine.cancelDeferredAssignment retires a PENDING row
// that way — and a node with an end and no beginning is not a shorter bar on a
// timeline, it is an unplaceable one. A renderer either drops it or anchors it
// at zero, and zero is 1970.
func withSpan(n Node, start, end string) Node {
	n.OccurredAt = normaliseInstant(start)
	if n.OccurredAt == "" {
		return n
	}
	n.EndedAt = normaliseInstant(end)
	n.DurationMS = spanMS(start, end)
	return n
}

// withInstant stamps only "when it happened", for a kind that is a datable
// event with no span of its own.
func withInstant(n Node, at string) Node {
	n.OccurredAt = normaliseInstant(at)
	return n
}
