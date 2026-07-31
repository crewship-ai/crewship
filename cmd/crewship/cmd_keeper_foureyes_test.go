package main

// cmd_keeper_foureyes_test.go — issue #1559.
//
// `crewship keeper status` printed the STORED second-approver toggle and
// nothing else, so an operator reading "2nd approver: off" had been told the
// opposite of what the resolve endpoint does to a top-tier credential. The
// human output must report the rule in force, and name which of the two
// sources puts it there.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestPrintKeeperGovernance_ReportsTheEffectiveSecondApproverRule(t *testing.T) {
	tierFloor, ok := keeper.MinSecondApproverLevel()
	if !ok {
		t.Fatal("no tier forces a second approver — the control this test exists for is gone, which is a failure, not a reason to skip")
	}

	t.Run("toggle off: the tier floor is still named", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			printKeeperGovernance(keeperGovernance{
				Configured: true,
				EffectiveSecondApprover: keeperEffectiveSecondApprover{
					MinSecurityLevel:      int(tierFloor),
					MinSecurityLevelLabel: tierFloor.Label(),
					Source:                "tier",
				},
			})
		})
		if !strings.Contains(out, "In force:") {
			t.Fatalf("no effective-rule line in:\n%s", out)
		}
		if !strings.Contains(out, tierFloor.Label()) {
			t.Errorf("effective line does not name %q:\n%s", tierFloor.Label(), out)
		}
		// The whole point: "off" must not be the last word an operator reads.
		if !strings.Contains(out, "tier") {
			t.Errorf("effective line does not say the tier is what forces it:\n%s", out)
		}
	})

	t.Run("toggle on: every credential escalation", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			printKeeperGovernance(keeperGovernance{
				Configured:            true,
				RequireSecondApprover: true,
				EffectiveSecondApprover: keeperEffectiveSecondApprover{
					MinSecurityLevel:      1,
					MinSecurityLevelLabel: keeper.SecurityLevels()[0].Label(),
					Source:                "workspace",
				},
			})
		})
		if !strings.Contains(out, "In force:") || !strings.Contains(out, "every credential escalation") {
			t.Errorf("toggle-on effective line = %q", out)
		}
	})

	t.Run("older server: no effective block, no invented line", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			printKeeperGovernance(keeperGovernance{Configured: true})
		})
		// A CLI newer than its server must say nothing rather than guess: the
		// tier table it would guess from is the server's, not this binary's.
		if strings.Contains(out, "In force:") {
			t.Errorf("printed an effective rule the server never sent:\n%s", out)
		}
		if !strings.Contains(out, "2nd approver:") {
			t.Errorf("dropped the stored toggle line:\n%s", out)
		}
	})
}

// The field has to survive the wire, not just the printer: a struct that
// silently fails to decode effective_second_approver would print nothing and
// look exactly like the older-server case above.
func TestKeeperStatusRunE_DecodesTheEffectiveRule(t *testing.T) {
	m := startKeeperMock(t)
	m.mu.Lock()
	m.gov["require_second_approver"] = false
	m.gov["effective_second_approver"] = map[string]any{
		"min_security_level":       4,
		"min_security_level_label": "L4 · critical",
		"source":                   "tier",
	}
	m.mu.Unlock()

	out := covCaptureStdoutCli3(t, func() {
		if err := keeperStatusCmd.RunE(keeperStatusCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "L4 · critical") {
		t.Errorf("status output did not carry the server's effective rule:\n%s", out)
	}
}
