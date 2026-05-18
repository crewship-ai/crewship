package api

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// metrics_handler.go — parseDBTime.
//
// Called from every metrics filler (metrics_fillers_runs_missions.go) to
// turn a SQLite-emitted timestamp string into a Go time.Time. SQLite's
// strftime variants and Go's RFC3339 marshalling can emit any of three
// shapes depending on which migration / Go writer produced the column,
// so this helper has to absorb all three and refuse the rest. A
// regression that broke any one shape would silently nuke a series in
// the metrics dashboard.
// ---------------------------------------------------------------------------

func TestParseDBTime_AcceptsRFC3339(t *testing.T) {
	// RFC3339 with explicit Z. The first branch — matches what Go's
	// time.Time.Format(time.RFC3339) emits when the row was written via
	// the orm path.
	got, err := parseDBTime("2026-05-18T10:20:30Z")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := time.Date(2026, 5, 18, 10, 20, 30, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
}

func TestParseDBTime_NormalizesOffsetToUTC(t *testing.T) {
	// RFC3339 with a non-UTC offset must be normalized to UTC — every
	// downstream consumer assumes UTC. A regression that kept the local
	// offset would mis-bucket metrics rolled up by UTC day boundaries.
	got, err := parseDBTime("2026-05-18T12:00:00+02:00")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v (must convert +02:00 → UTC)", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
}

func TestParseDBTime_AcceptsSpaceSeparatedSQLite(t *testing.T) {
	// SQLite's CURRENT_TIMESTAMP and strftime('%Y-%m-%d %H:%M:%S')
	// produce this shape — space separator, no zone. Treated as UTC
	// because every writer in the project stores UTC.
	got, err := parseDBTime("2026-05-18 10:20:30")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := time.Date(2026, 5, 18, 10, 20, 30, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDBTime_AcceptsTSeparatedNoZone(t *testing.T) {
	// Third fallback: ISO-8601 with T separator but NO zone marker.
	// Some older rows wrote this shape before the RFC3339-everywhere
	// convention landed; we still have to read them.
	got, err := parseDBTime("2026-05-18T10:20:30")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	want := time.Date(2026, 5, 18, 10, 20, 30, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDBTime_RejectsEmpty(t *testing.T) {
	// Empty string is a distinct, common case — comes from SELECTs where
	// a NULL timestamp slipped through into a string scan. Source
	// returns errors.New("empty time string") — pin the message so a
	// caller-side error log keeps its triage value.
	_, err := parseDBTime("")
	if err == nil {
		t.Fatal("expected error on empty string")
	}
	if !strings.Contains(err.Error(), "empty time string") {
		t.Errorf("err = %v, want \"empty time string\"", err)
	}
}

func TestParseDBTime_RejectsGarbage_QuotesValueInError(t *testing.T) {
	// The catch-all path quotes the offending value with %q so an
	// operator scanning the metrics-handler log can grep the exact
	// timestamp. A regression that dropped %q (or switched to %s) would
	// hide trailing whitespace / control chars that are exactly what
	// breaks parsing in practice.
	bad := "not-a-time"
	_, err := parseDBTime(bad)
	if err == nil {
		t.Fatal("expected error on garbage")
	}
	if !strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("err = %v, want \"unrecognised\" prefix", err)
	}
	if !strings.Contains(err.Error(), `"`+bad+`"`) {
		t.Errorf("err = %v, want value quoted with %%q for log greppability", err)
	}
}

func TestParseDBTime_RejectsDateOnly(t *testing.T) {
	// Date-only (no time-of-day) was never a supported writer output;
	// pin it explicitly so a future "accept dates too" change has to
	// flip this test in step and consider bucketing implications.
	_, err := parseDBTime("2026-05-18")
	if err == nil {
		t.Fatal("expected error on date-only input")
	}
}

func TestParseDBTime_RejectsPartiallyMalformed(t *testing.T) {
	// Inputs that look right but break on a single field — the same
	// shape SQLite-side string truncation would produce. Pin individual
	// cases so a regression in time.Parse's strictness (e.g. accepting
	// 1-digit days) surfaces here, not in a silently-wrong metric.
	// NOTE: sub-second precision (.123456789) is INTENTIONALLY NOT in
	// this list — Go's time.Parse with the bare "2006-01-02T15:04:05"
	// layout silently accepts a trailing fractional component. That's
	// fine for downstream consumers (the value still rounds correctly)
	// so we don't pin a rejection contract there.
	cases := []string{
		"2026-05-18T10:20:30+99:00", // impossible offset
		"2026-13-01T10:20:30Z",      // month 13
		"2026-05-18T25:00:00Z",      // hour 25
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := parseDBTime(in)
			if err == nil {
				t.Errorf("parseDBTime(%q) returned nil error; expected rejection", in)
			}
		})
	}
}

func TestParseDBTime_RoundTripWithFillerRangeEnd(t *testing.T) {
	// metrics_fillers_runs_missions.go formats range endpoints with
	// time.RFC3339 and round-trips them through parseDBTime. Pin that
	// the marshalling round-trips byte-for-equal-time, otherwise a
	// "now ± window" filter at the SQL layer drifts by parsing.
	now := time.Date(2026, 5, 18, 14, 30, 45, 0, time.UTC)
	round, err := parseDBTime(now.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("round-trip err = %v", err)
	}
	if !round.Equal(now) {
		t.Errorf("round-trip drift: in=%v out=%v", now, round)
	}
}
