// Package procgroupfixture builds the fake CLI that the process-group kill
// tests use.
//
// Used by internal/devcontainer and internal/provider/apple, whose
// procgroup_unix_test.go files each own a copy of
// TestKillProcessGroup_ReachesDescendantsOfAReapedChild. The fixture lives
// here rather than in either of them because the two copies already drifted
// into the same bug once (#2044) and the reasoning below is what stops it
// happening again — it is worth exactly one copy.
//
// Deliberately dependency-free: internal/testutil proper reaches
// internal/database, internal/mailer and internal/webhook, and neither test
// binary should grow that graph to obtain a shell script.
package procgroupfixture

import (
	"os"
	"path/filepath"
	"testing"
)

// Announcement is what the fake CLI writes to stdout once the holder exists.
const Announcement = "holding\n"

// script is the fake CLI: a process that backgrounds a helper which inherits
// stdout, then exits itself — leaving the write end of the pipe held by
// something exec.Cmd knows nothing about, which is the shape of the real
// container CLI and the reason #2030 could hang.
//
// THE ORDER OF THE TWO COMMANDS IS LOAD-BEARING. DO NOT "SIMPLIFY" IT TO
// `( echo holding; sleep 60 ) &`.
//
// The tests read Announcement and take it as proof that the holder is already
// a member of the process group, because the very next thing they do is kill
// that group and assert the holder died. Only this ordering makes the proof
// hold, and only on some shells does the obvious alternative:
//
//   - `( echo holding; sleep 60 ) &` — dash and bash 5.2 exec the last command
//     of a subshell in place, so the process that writes the announcement *is*
//     the one that becomes `sleep 60`: one process, already in the group.
//     macOS /bin/sh is bash 3.2.57, where execute_in_subshell sets CMD_NO_FORK
//     only for cm_simple and cm_subshell bodies; `echo holding; sleep 60` is a
//     cm_connection, so bash 3.2 forks a separate /bin/sleep *after* the write.
//     The announcement then races the fork, and on Darwin the kill can walk the
//     group before the holder has linked into it — the holder survives, keeps
//     the pipe, and the test fails on the post-kill read deadline (#2044).
//
//   - `( sleep 60 & echo holding ) &` — `&` returns in the subshell only after
//     the child has been forked, so the holder provably exists before anything
//     is announced, whatever the shell decided to fork. Traced under one shell
//     with bash 5.2's tail-exec optimization defeated, the old shape announces
//     ~460us before the holder is cloned and the new shape clones it ~430us
//     before announcing.
//
// Note that a Linux run cannot tell these two apart: there, a kill landing in
// the gap kills the subshell before it forks, which is harmless. Only a Darwin
// host exercises the failing case.
const script = "#!/bin/sh\n( sleep 60 & echo holding ) &\n"

// WriteFakeCLI writes the fake CLI into dir and returns its path.
func WriteFakeCLI(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "container")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
