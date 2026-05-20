package main

import "testing"

// These helpers shape the per-step cost / duration columns in
// `crewship routine logs <run_id> --slug X`. The CLI mirrors the
// Runs-tab waterfall in the UI (components/features/routines/
// routine-cost-format.ts); the test fixtures here intentionally
// match the TS test fixtures so a drift in either surface surfaces
// here too.

func TestFormatPayloadCost(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want string
	}{
		{"nil payload", nil, "—"},
		{"missing field", map[string]interface{}{"step_id": "x"}, "—"},
		{"zero cost", map[string]interface{}{"cost_usd": 0.0}, "—"},
		{"negative cost", map[string]interface{}{"cost_usd": -0.01}, "—"},
		// JSON unmarshals numbers to float64 — that's the production path.
		{"float64 cost", map[string]interface{}{"cost_usd": 0.0123}, "$0.0123"},
		{"micro-cost stays legible", map[string]interface{}{"cost_usd": 0.0001}, "$0.0001"},
		{"dollar-scale cost", map[string]interface{}{"cost_usd": 1.5}, "$1.5000"},
		// Defensive: a future schema change might emit an int — don't
		// drop precision silently.
		{"int cost", map[string]interface{}{"cost_usd": 2}, "$2.0000"},
		// Wrong type → em-dash, NOT a panic.
		{"string cost rejected", map[string]interface{}{"cost_usd": "0.05"}, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPayloadCost(tc.in); got != tc.want {
				t.Errorf("formatPayloadCost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatPayloadDuration(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]interface{}
		want string
	}{
		{"nil payload", nil, "—"},
		{"missing field", map[string]interface{}{"step_id": "x"}, "—"},
		{"zero duration", map[string]interface{}{"duration_ms": 0.0}, "—"},
		{"negative duration", map[string]interface{}{"duration_ms": -1.0}, "—"},
		// Sub-second → integer ms.
		{"single-digit ms", map[string]interface{}{"duration_ms": 7.0}, "7ms"},
		{"sub-second", map[string]interface{}{"duration_ms": 999.0}, "999ms"},
		// 1s ≤ d < 10s → 2-decimal seconds.
		{"sub-10s 2dp", map[string]interface{}{"duration_ms": 1234.0}, "1.23s"},
		// 10s ≤ d < 60s → 1-decimal seconds.
		{"sub-minute 1dp", map[string]interface{}{"duration_ms": 12345.0}, "12.3s"},
		{"59s 1dp", map[string]interface{}{"duration_ms": 59000.0}, "59.0s"},
		// ≥ 60s → minute:second.
		{"1m flat", map[string]interface{}{"duration_ms": 60000.0}, "1m00s"},
		{"2m05s", map[string]interface{}{"duration_ms": 125000.0}, "2m05s"},
		{"10m10s", map[string]interface{}{"duration_ms": 610000.0}, "10m10s"},
		// Defensive: int path.
		{"int ms", map[string]interface{}{"duration_ms": 500}, "500ms"},
		// Wrong type → em-dash, not panic.
		{"string ms rejected", map[string]interface{}{"duration_ms": "100"}, "—"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPayloadDuration(tc.in); got != tc.want {
				t.Errorf("formatPayloadDuration = %q, want %q", got, tc.want)
			}
		})
	}
}
