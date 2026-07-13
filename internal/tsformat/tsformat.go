// Package tsformat provides the canonical fixed-width timestamp format
// for values we WRITE into TEXT columns that SQL later compares or
// orders lexicographically (#990).
//
// Why not time.RFC3339Nano: it TRUNCATES trailing zeros in the
// fractional seconds, so two timestamps inside the same second can
// render at different widths and string-compare in the wrong order —
// "…02.5Z" sorts AFTER "…02.500123456Z" because 'Z' (0x5A) > '0'
// (0x30). The quartermaster online sampler's scan-window bounds hit
// exactly this: a run whose ended_at had trailing-zero nanos on the
// scanEnd boundary was skipped for a tick (and the same family flaked
// the cross-platform CI matrix ~1/40).
//
// The layout below always renders 9 fractional digits and normalizes
// to UTC, so equal-width strings sort exactly like the times they
// encode. time.RFC3339Nano PARSING accepts this form unchanged, so
// readers don't need to migrate.
//
// Rule of thumb: any `.Format(time.RFC3339Nano)` whose result lands in
// a SQL comparison or ORDER BY belongs here instead. (Rows written
// before this package may still carry truncated fractions; comparisons
// against them can be off within one shared second — self-healing for
// the sampler because its watermark never advances past an unhandled
// row.)
package tsformat

import "time"

// Layout is RFC 3339 with a fixed 9-digit fractional second — the
// lexicographically-sortable variant of time.RFC3339Nano.
const Layout = "2006-01-02T15:04:05.000000000Z07:00"

// Format renders t in UTC using Layout. The output is fixed-width, so
// string order == time order, and parses back via time.RFC3339Nano.
func Format(t time.Time) string {
	return t.UTC().Format(Layout)
}
