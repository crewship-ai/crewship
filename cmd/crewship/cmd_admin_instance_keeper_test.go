//go:build !clionly

package main

import "testing"

func TestGovValueNamesTheUnsetCadence(t *testing.T) {
	cases := []struct {
		field string
		v     any
		want  string
	}{
		{"behavior_sample_every", float64(0), "default (1 in 5)"},
		{"behavior_sample_every", float64(10), "10"},
		{"enabled", false, "false"},
		{"deny_notify_min_risk", float64(0), "0"},
	}
	for _, c := range cases {
		if got := govValue(c.field, c.v); got != c.want {
			t.Errorf("govValue(%s, %v) = %q, want %q", c.field, c.v, got, c.want)
		}
	}
}
