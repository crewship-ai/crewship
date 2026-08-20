package main

// The twin of internal/backup's TestRestoreCrew_PreflightNamesARealCommand.
//
// A restore preflight failure tells the operator to run
// `crewship crew provision <crew>` — every restore section goes through
// exec-tar now, so a stopped container fails the preflight, and
// provision is what reconciles a stopped container back to running.
//
// The message lives in internal/backup, which cannot see this command
// table; this table lives here, which cannot see the message. Nothing
// tied them together, and the version before this one named
// `crewship crew start`, which has never existed — so the documented
// remediation ended at "unknown command". These two tests are the tie:
// rename or drop `provision` and this fails, change the message and its
// twin fails.

import (
	"strings"
	"testing"
)

func TestCrewProvisionExists_BecauseTheRestorePreflightNamesIt(t *testing.T) {
	names := map[string]bool{}
	for _, c := range crewCmd.Commands() {
		names[c.Name()] = true
		for _, alias := range c.Aliases {
			names[alias] = true
		}
	}

	if !names["provision"] {
		var have []string
		for n := range names {
			have = append(have, n)
		}
		t.Fatalf("`crewship crew provision` is not registered, but internal/backup's restore preflight tells operators to run it. Registered: %s",
			strings.Join(have, ", "))
	}

	// `crew start` has since been added, and the decision this tripwire
	// asked for was made: internal/backup/restorer.go's preflight now
	// names it, because the container being stopped is exactly what
	// start fixes and provision only ever fixed by accident. Both
	// commands must stay registered — the preflight names start, and
	// image-only rebuild flows elsewhere still name provision.
	if !names["start"] {
		t.Errorf("`crewship crew start` is not registered, but internal/backup's restore preflight and the crew-file 409 both tell operators to run it")
	}
}
