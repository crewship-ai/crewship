package main

// CLI parity for the behaviour-monitor sampling rate (#1001 M3) — the contract
// agents use. Drives the real cobra commands against the keeper mock.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/behaviorhook"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

func TestKeeperSamplingCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["sampling"] {
		t.Fatalf("keeper missing subcommand %q; have %v", "sampling", have)
	}

	haveSub := map[string]bool{}
	for _, sub := range keeperSamplingCmd.Commands() {
		haveSub[sub.Name()] = true
	}
	for _, want := range []string{"status", "set", "default"} {
		if !haveSub[want] {
			t.Errorf("keeper sampling missing subcommand %q; have %v", want, haveSub)
		}
	}
}

// TestKeeperSamplingSetRunE_SendsFieldOnly proves the CLI drives the shared
// partial-update PUT rather than a read-merge-write, so a concurrent edit to an
// unrelated governance field can't be clobbered.
func TestKeeperSamplingSetRunE_SendsFieldOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperSamplingSetCmd.RunE(keeperSamplingSetCmd, []string{"3"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// JSON numbers decode as float64 through the mock's generic map.
	assertPartialPut(t, m, "behavior_sample_every", float64(3))
}

// TestKeeperSamplingDefaultRunE_SendsTheDefault: "back to default" writes the
// default explicitly rather than clearing the field, so the row records what
// this workspace decided instead of reverting to "never configured".
func TestKeeperSamplingDefaultRunE_SendsTheDefault(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperSamplingDefaultCmd.RunE(keeperSamplingDefaultCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "behavior_sample_every", float64(governance.DefaultBehaviorSampleEvery))
}

// TestKeeperSamplingSetRunE_RejectsOutOfRange keeps bad values off the wire.
// Zero is the one that matters: it is what an operator types when they mean
// "stop", and accepting it would leave the workspace reading "watchdog enabled"
// while nothing was ever reviewed. The error has to name the real off switch.
func TestKeeperSamplingSetRunE_RejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		arg     string
		wantSub string
	}{
		{"not a number", "often", "whole number"},
		{"zero is not off", "0", "keeper disable"},
		{"negative", "-1", "between"},
		{"above the ceiling", "101", "between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No mock server started: a rejected argument must fail before any
			// HTTP call, so reaching the network here would itself be the bug.
			err := keeperSamplingSetCmd.RunE(keeperSamplingSetCmd, []string{tc.arg})
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestKeeperSamplingBoundsMatchServer pins the CLI's client-side validation and
// its help text to the server's constants — if they drift, the CLI either
// refuses values the server accepts or lets through ones it won't.
func TestKeeperSamplingBoundsMatchServer(t *testing.T) {
	t.Parallel()

	if governance.MinBehaviorSampleEvery != 1 {
		t.Errorf("MinBehaviorSampleEvery = %d; the CLI help and docs say 1 (review every call)",
			governance.MinBehaviorSampleEvery)
	}
	if governance.MaxBehaviorSampleEvery != 100 {
		t.Errorf("MaxBehaviorSampleEvery = %d; the CLI help and docs say 100",
			governance.MaxBehaviorSampleEvery)
	}
}

// TestSamplingDefaultMatchesTheHook is the drift guard the two constants need:
// governance owns the number the API/CLI/console show, behaviorhook owns the
// number the monitor actually runs on, and neither package can import the
// other. If they diverge, every surface starts describing a cadence the hook is
// not using.
func TestSamplingDefaultMatchesTheHook(t *testing.T) {
	t.Parallel()

	if governance.DefaultBehaviorSampleEvery != behaviorhook.DefaultSampleEvery {
		t.Errorf("governance.DefaultBehaviorSampleEvery = %d but behaviorhook.DefaultSampleEvery = %d — "+
			"the config surface would advertise a cadence the monitor does not run on",
			governance.DefaultBehaviorSampleEvery, behaviorhook.DefaultSampleEvery)
	}
}

// TestFormatSampleEvery covers the human rendering. The unset sentinel is the
// interesting case: printing a bare 0 would read as "never".
func TestFormatSampleEvery(t *testing.T) {
	t.Parallel()

	if got := formatSampleEvery(0); !strings.Contains(got, "default") {
		t.Errorf("formatSampleEvery(0) = %q, want it to name the default", got)
	}
	if got := formatSampleEvery(1); !strings.Contains(got, "every tool call") {
		t.Errorf("formatSampleEvery(1) = %q, want it to say every tool call", got)
	}
	if got := formatSampleEvery(20); !strings.Contains(got, "1 in 20") {
		t.Errorf("formatSampleEvery(20) = %q, want a 1-in-N rate", got)
	}
}
