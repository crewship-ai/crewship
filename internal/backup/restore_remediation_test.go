package backup

import (
	"strings"
	"testing"
)

// Every restore remediation has to name a command that leaves the
// operator where restore needs them, which is a crew container that is
// RUNNING — exec-tar is how every filesystem section is written.
//
// They all named `crewship crew provision`, which builds an image and
// creates nothing. Following one of these messages therefore got you as
// far as the NEXT error: the preflight's "the crew container must be
// RUNNING". Two commands deep, both of them named by us, neither of
// them the one that works.
//
// `crewship crew start` provisions the image if there is none and then
// starts the container, so it is the whole remedy in one command and
// correct for both the never-provisioned crew and the stopped one.
//
// Pinned as a test rather than left to review because the failure is
// invisible in the diff: `provision` reads plausibly in all three
// sentences, and only someone who has followed one end to end knows it
// stops short.
func TestRestoreRemediationsNameACommandThatCreatesAContainer(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  string
	}{
		{"preflight (stopped container)", restorePreflightRemediation("/crew/shared", "alpha")},
		{"missing container", restoreMissingContainerRemediation("alpha", "crew-alpha")},
		{"files-only landed nothing", restoreFilesOnlyEmptyRemediation(2, "ws-1")},
		{"docker phase skipped", restoreForkedWorkspaceRemediation()},
		{"forked-restore step 1", ForkedRestoreSteps("alpha", "ws-1", "b.tar.zst")[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.msg, "crewship crew start") {
				t.Errorf("remediation does not name the command that gets a container running:\n%s", tc.msg)
			}
			if strings.Contains(tc.msg, "crewship crew provision") {
				t.Errorf("remediation still sends the operator to `provision`, which builds an "+
					"image and starts nothing — they land on the next error:\n%s", tc.msg)
			}
		})
	}
}

// The CLI prints its own multi-line version of the forked-restore advice
// with the real slugs filled in, and THAT copy is the one an operator
// reads — restoreForkedWorkspaceRemediation only reaches the server log.
// They drifted apart once: the server text was corrected to `crew start`
// while the CLI kept printing `crew provision`, so the most visible
// instance of the bug survived its own fix. Both now build from
// ForkedRestoreSteps; this pins the substitution so a caller cannot
// quietly reintroduce a hand-written copy.
func TestForkedRestoreSteps_SubstitutesAndOrdersTheCommands(t *testing.T) {
	steps := ForkedRestoreSteps("engineering", "ws_new123", "bundle.tar.zst")

	if steps[0] != "crewship crew start engineering -w ws_new123" {
		t.Errorf("step 1 = %q", steps[0])
	}
	if steps[1] != "crewship backup restore bundle.tar.zst -w ws_new123 --files-only" {
		t.Errorf("step 2 = %q", steps[1])
	}
	// Order matters: --files-only writes by exec'ing into the container,
	// so the crew has to be up before the restore, not after.
	if !strings.Contains(steps[0], "crew start") || !strings.Contains(steps[1], "--files-only") {
		t.Errorf("steps are out of order: %v", steps)
	}
}
