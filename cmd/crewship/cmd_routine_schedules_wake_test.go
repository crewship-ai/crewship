package main

import "testing"

func TestWakeCell(t *testing.T) {
	cases := []struct {
		name string
		row  scheduleRow
		want string
	}{
		{"ungated", scheduleRow{}, "—"},
		{"gated, never checked", scheduleRow{WakePipelineSlug: "cost-probe"}, "cost-probe"},
		{"gated with telemetry", scheduleRow{WakePipelineSlug: "cost-probe", WakeCheckCount: 96, WakeFireCount: 3}, "cost-probe 3/96"},
		{"slug missing, id fallback", scheduleRow{WakePipelineID: "pln_probe"}, "pln_probe"},
	}
	for _, c := range cases {
		if got := wakeCell(c.row); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
