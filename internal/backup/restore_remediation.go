package backup

import "fmt"

// The sentences restore prints when it cannot land a crew's filesystem.
//
// Gathered here from four call sites so they can be read — and tested —
// as one surface. They had drifted into naming `crewship crew
// provision`, which builds an image and creates no container, while
// every one of these failures needs a container that is RUNNING: since
// exec-tar became how filesystem sections are written, restore cannot
// write into a crew it cannot exec into. Following one of these got the
// operator to the next error rather than to a restored crew.
//
// `crewship crew start` provisions the image when there is none and
// then starts the container, so it is the whole remedy in one command
// and correct for the never-provisioned crew as well as the stopped
// one. restore_remediation_test.go pins that, because the mistake is
// invisible in review: `provision` reads plausibly in all four
// sentences and only stops short when you follow it.

// restorePreflightRemediation is the refusal when the crew container
// cannot be exec'd into at all — almost always a stopped one.
func restorePreflightRemediation(dest, crewSlug string) string {
	return fmt.Sprintf(
		"cannot reach %s to restore into it — the crew container must be RUNNING (exec is how every section is written now). Start it with `crewship crew start %s`",
		dest, crewSlug)
}

// restoreMissingContainerRemediation is the preflight refusal when the
// crew has filesystem data in the bundle but no container exists for it
// on this instance.
func restoreMissingContainerRemediation(crewSlug, containerID string) string {
	return fmt.Sprintf(
		"backup: crew %q has filesystem data in the bundle but has no container %q on this instance; run `crewship crew start %s` then re-run restore",
		crewSlug, containerID, crewSlug)
}

// restoreFilesOnlyEmptyRemediation is the --files-only refusal when no
// crew in the bundle had somewhere to land.
func restoreFilesOnlyEmptyRemediation(crewCount int, workspace string) string {
	return fmt.Sprintf(
		"backup: --files-only landed no crew filesystem state: the bundle's %d crew(s) either carry no filesystem sections or have no running container on this instance — start them with `crewship crew start <crew> -w %s` and re-run",
		crewCount, workspace)
}

// restoreForkedWorkspaceRemediation is the note logged when
// --as-workspace / --as-crew rewrote the slugs, so the docker phase was
// skipped and the operator has to land files as a second pass.
func restoreForkedWorkspaceRemediation() string {
	steps := ForkedRestoreSteps("<crew>", "<workspace>", "<bundle>")
	return "docker phase skipped because --as-workspace / --as-crew was supplied. " +
		"Start the new crews (`" + steps[0] + "`), then land their files with " +
		"`" + steps[1] + "`. " +
		"Re-running restore WITHOUT --files-only will not work and is not what to do: the bundle is not bound to the forked workspace, " +
		"and its rows are already there under new ids"
}

// ForkedRestoreSteps returns the two commands that finish a
// --as-workspace / --as-crew restore, in order.
//
// Exported because the CLI prints its own multi-line version of this
// advice with the real slugs filled in, and that copy is the one the
// operator actually reads — the sentence above only reaches the server
// log. They drifted apart once already: the server-side text was
// corrected to `crew start` while the CLI went on printing `crew
// provision`, so the most visible instance of the bug survived the fix
// and then contradicted the server it was talking to. One source for the
// command names, two presentations.
func ForkedRestoreSteps(crew, workspace, bundle string) [2]string {
	return [2]string{
		fmt.Sprintf("crewship crew start %s -w %s", crew, workspace),
		fmt.Sprintf("crewship backup restore %s -w %s --files-only", bundle, workspace),
	}
}
