package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

func TestKeeperAutoLeaseCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["auto-lease"] {
		t.Fatalf("keeper missing subcommand %q; have %v", "auto-lease", have)
	}

	haveAL := map[string]bool{}
	for _, sub := range keeperAutoLeaseCmd.Commands() {
		haveAL[sub.Name()] = true
	}
	for _, want := range []string{"status", "set", "off"} {
		if !haveAL[want] {
			t.Errorf("keeper auto-lease missing subcommand %q; have %v", want, haveAL)
		}
	}
}

// TestKeeperAutoLeaseSetRunE_SendsFieldOnly proves the CLI drives the shared
// partial-update PUT rather than a read-merge-write, so a concurrent edit to an
// unrelated governance field can't be clobbered — and that a Go duration is
// converted to the seconds the API expects.
func TestKeeperAutoLeaseSetRunE_SendsFieldOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperAutoLeaseSetCmd.RunE(keeperAutoLeaseSetCmd, []string{"15m"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// JSON numbers decode as float64 through the mock's generic map.
	assertPartialPut(t, m, "auto_lease_seconds", float64(900))
}

func TestKeeperAutoLeaseOffRunE_SendsZero(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperAutoLeaseOffCmd.RunE(keeperAutoLeaseOffCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	assertPartialPut(t, m, "auto_lease_seconds", float64(0))
}

// TestKeeperAutoLeaseSetRunE_RejectsOutOfRange keeps the bad values from ever
// reaching the wire. The sub-minute case is the important one: a lease shorter
// than Keeper's own LLM evaluation would lapse before the ALLOW it authorises
// reaches the injection point, so the ALLOW would deny itself.
func TestKeeperAutoLeaseSetRunE_RejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		arg     string
		wantSub string
	}{
		{"not a duration", "soon", "invalid duration"},
		{"below the floor", "5s", "at least"},
		{"zero", "0s", "at least"},
		// Go durations have no "d" unit, so 31 days is 745h.
		{"above the cap", "745h", "at most 30 days"},
		{"days unit is not a Go duration", "31d", "invalid duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No mock server started: a rejected argument must fail before any
			// HTTP call, so reaching the network here would itself be the bug.
			err := keeperAutoLeaseSetCmd.RunE(keeperAutoLeaseSetCmd, []string{tc.arg})
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.arg)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestKeeperAutoLeaseBoundsMatchServer pins the CLI's client-side validation to
// the server's constants. If they drift, the CLI either rejects values the
// server would accept or lets through ones it won't.
func TestKeeperAutoLeaseBoundsMatchServer(t *testing.T) {
	t.Parallel()

	if governance.MinAutoLeaseSeconds != 60 {
		t.Errorf("MinAutoLeaseSeconds = %d; the CLI help and docs say 60s",
			governance.MinAutoLeaseSeconds)
	}
	if governance.MaxAutoLeaseSeconds != 30*24*60*60 {
		t.Errorf("MaxAutoLeaseSeconds = %d; the CLI help and docs say 30 days",
			governance.MaxAutoLeaseSeconds)
	}
}

// TestFormatAutoLease covers the human rendering: an operator reading
// `keeper status` must see a duration, not a raw second count.
func TestFormatAutoLease(t *testing.T) {
	t.Parallel()

	if got := formatAutoLease(0); !strings.Contains(got, "off") {
		t.Errorf("formatAutoLease(0) = %q, want it to say off", got)
	}
	if got := formatAutoLease(-5); !strings.Contains(got, "off") {
		t.Errorf("formatAutoLease(-5) = %q, want it to say off", got)
	}
	if got := formatAutoLease(900); !strings.Contains(got, "15m") {
		t.Errorf("formatAutoLease(900) = %q, want it to render 15m", got)
	}
}
