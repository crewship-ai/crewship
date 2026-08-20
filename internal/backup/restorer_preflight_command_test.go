package backup

// The preflight's remediation has to name a command that exists.
//
// Every restore section now goes through exec-tar, so a stopped
// container fails the preflight — and the message told the operator to
// run `crewship crew start`, which is not a registered command. The CLI
// has provision, rebuild, restart-agents, status, container-status,
// services, files, cache, config and connect; no start. So the runbook
// this change prints led to a preflight failure whose remediation
// errored with "unknown command".
//
// `crew provision` is the one that reconciles a stopped container back
// to running — it logs "restarted stopped container" for exactly this
// case. Its twin in cmd/crewship asserts the command is really
// registered, so renaming either side breaks the pair rather than
// leaving this message pointing at nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRestoreCrew_PreflightNamesARealCommand(t *testing.T) {
	ctx := context.Background()
	p := fullSectionPayload(t)

	// A container that cannot be exec'd is what a stopped one looks like
	// from here.
	ops := &restoreRecOps{execAsErr: errors.New("Container 123 is not running")}

	err := RestoreCrew(ctx, ops, "c1", "alpha", p)
	if err == nil {
		t.Fatal("restore into an unreachable container reported success")
	}
	if !errors.Is(err, ErrRestorePreflight) {
		t.Fatalf("must refuse in the preflight, before writing: %v", err)
	}
	// `crewship crew start` starts the container (and provisions its
	// image first if there is none), so it is both a command that exists
	// and the one that fixes this. It replaced `crew provision` here,
	// which only ever reconciled a stopped container back to running as
	// a side effect and mostly just built an image.
	if !strings.Contains(err.Error(), "crewship crew start") {
		t.Errorf("remediation must name the command that starts a crew; got %v", err)
	}
	// The operator also has to be told WHY, or "run provision" reads as
	// a guess rather than the fix.
	if !strings.Contains(err.Error(), "RUNNING") {
		t.Errorf("message should say the container must be running: %v", err)
	}
}
