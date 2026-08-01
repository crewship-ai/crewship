package orchestrator

import (
	"strings"
	"testing"
)

// sidecar.log used to be an unbounded append.
//
// The sidecar's stderr was redirected with `2>>/tmp/sidecar.log` and nothing
// ever trimmed it. /tmp is a tmpfs, and tmpfs pages are charged to the crew's
// memory cgroup, so a crew container that stays up for days slowly eats its own
// memory limit and is eventually OOM-killed — which surfaces as "the agent
// died", not as "a log grew". crew-runtime-capacity.md §5 item 2.
//
// This file asserts on the launch script's TEXT and on the constants. The
// assertions that run the trim against real bytes live next door in
// exec_sidecar_logcap_unix_test.go, which is build-tagged !windows because the
// trim is /bin/sh — a build tag rather than a t.Skip, so a platform that cannot
// run them fails to compile the file instead of reporting a green test that
// never ran (scripts/skip-budget.sh).

func TestSidecarLaunchScript_TruncatesTheLogOnStart(t *testing.T) {
	s := sidecarLaunchScript("Y3JlZHM=")

	if !strings.Contains(s, ": >"+sidecarLogPath) {
		t.Errorf("the launch script does not truncate %s before starting the sidecar, "+
			"so a restarted sidecar inherits every byte the previous one wrote:\n%s",
			sidecarLogPath, s)
	}
	// Truncate-then-append, not truncate-on-redirect: the trimmer below rewrites
	// the file in place, and only an O_APPEND writer (`2>>`) reseeks to the new
	// end. A plain `2>` writer would keep its old offset and refill the gap with
	// a sparse hole, defeating the cap.
	if !strings.Contains(s, "2>>"+sidecarLogPath) {
		t.Errorf("the sidecar's stderr is not opened in append mode; in-place trimming "+
			"needs O_APPEND or the file grows back as a sparse hole:\n%s", s)
	}
}

func TestSidecarLaunchScript_CapsTheLogSize(t *testing.T) {
	s := sidecarLaunchScript("Y3JlZHM=")

	if !strings.Contains(s, "wc -c") {
		t.Errorf("nothing in the launch script measures the log, so nothing can cap it:\n%s", s)
	}
	if !strings.Contains(s, sidecarLogMaxBytesStr) {
		t.Errorf("the launch script does not enforce the %s-byte cap:\n%s", sidecarLogMaxBytesStr, s)
	}
	if sidecarLogKeepBytes >= sidecarLogMaxBytes {
		t.Fatalf("keep (%d) must be smaller than the cap (%d) or trimming is a no-op",
			sidecarLogKeepBytes, sidecarLogMaxBytes)
	}
}

// The cap must keep the NEWEST output. A `mv`-style rotation would be worse
// than the leak: the sidecar holds an open fd, so after a rename it keeps
// writing into an unlinked inode whose pages are still charged to the cgroup
// and whose contents nobody can read again.
func TestSidecarLaunchScript_TrimKeepsTheNewestOutput(t *testing.T) {
	s := sidecarLaunchScript("Y3JlZHM=")

	if !strings.Contains(s, "tail -c "+sidecarLogKeepBytesStr) {
		t.Errorf("the trim does not keep the newest %s bytes:\n%s", sidecarLogKeepBytesStr, s)
	}
	for _, banned := range []string{"mv " + sidecarLogPath, "rm -f " + sidecarLogPath + " "} {
		if strings.Contains(s, banned) {
			t.Errorf("the trim uses %q, which unlinks the inode the sidecar is still "+
				"writing to — the output becomes invisible and the pages stay charged:\n%s",
				banned, s)
		}
	}
	// Rewrite in place so the sidecar's fd stays valid.
	if !strings.Contains(s, "cat "+sidecarLogPath+".tail >"+sidecarLogPath) {
		t.Errorf("the trim does not rewrite %s in place:\n%s", sidecarLogPath, s)
	}
}

// The trimmer is a background loop; it must not outlive the sidecar it trims,
// or a crew that restarts its sidecar accumulates one orphaned loop per restart
// against PidsLimit.
func TestSidecarLaunchScript_TrimmerExitsWithTheSidecar(t *testing.T) {
	s := sidecarLaunchScript("Y3JlZHM=")

	if !strings.Contains(s, "$!") {
		t.Errorf("the launch script never captures the sidecar PID, so the trimmer "+
			"cannot tell when to stop:\n%s", s)
	}
	if !strings.Contains(s, "kill -0") {
		t.Errorf("the trimmer never checks whether the sidecar is still alive:\n%s", s)
	}
}

// The credentials still arrive base64-encoded on stdin, and the health check
// still decides the exec's exit code — the log cap must not have disturbed
// either.
func TestSidecarLaunchScript_KeepsCredsOnStdinAndHealthCheck(t *testing.T) {
	s := sidecarLaunchScript("QUJD")

	if !strings.Contains(s, "echo 'QUJD' | base64 -d | crewship-sidecar") {
		t.Errorf("credentials no longer reach the sidecar over stdin:\n%s", s)
	}
	if !strings.Contains(s, "/health") || !strings.Contains(s, "exit 1") {
		t.Errorf("the health check no longer fails the exec:\n%s", s)
	}
}

// Bound the constants themselves.
//
// The behavioural tests (exec_sidecar_logcap_unix_test.go) execute the trim,
// but every one of them is written against a fixed 2 MiB fixture, and they do
// not build on Windows. This is the cheap belt that runs everywhere and says
// the cap may not wander to a value that caps nothing (1<<62 was the mutation
// that survived the original, text-only suite) nor to one so small the log is
// useless.
func TestSidecarLogCap_StaysInASaneRange(t *testing.T) {
	// Floor: a single Go panic with a few goroutine stacks is tens of KiB, and
	// a cap below that turns every incident into a truncated trace.
	const floor = 64 << 10
	// Ceiling: the cap is charged PER CREW against the crew's memory cgroup,
	// and the fleet target is 20–50 crews per host (crew-runtime-capacity.md
	// §5). Even at 4 MiB that is ~200 MB of tmpfs fleet-wide. Anything above
	// this is not a cap, it is a slower leak.
	//
	// The behavioural tests are stricter — their 2 MiB fixture must actually be
	// trimmed, so they reject anything from 2 MiB up. This range is the belt to
	// that braces: it fails loudly on the value, they fail on the effect.
	const ceiling = 4 << 20

	if sidecarLogMaxBytes < floor || sidecarLogMaxBytes > ceiling {
		t.Errorf("sidecarLogMaxBytes = %d, outside the sane range [%d, %d]. Above the "+
			"ceiling the cap no longer bounds the crew's tmpfs charge, which is the whole "+
			"reason it exists; below the floor a single stack trace does not fit.",
			sidecarLogMaxBytes, floor, ceiling)
	}
	if sidecarLogKeepBytes >= sidecarLogMaxBytes {
		t.Errorf("keep (%d) must be smaller than the cap (%d) or every trim is a no-op",
			sidecarLogKeepBytes, sidecarLogMaxBytes)
	}
	if sidecarLogKeepBytes < 4<<10 {
		t.Errorf("keep (%d) is too small to carry any context past a trim", sidecarLogKeepBytes)
	}
	// The check is periodic, so the real bound is cap + one interval of stderr.
	// An hour between checks makes the cap nominal.
	if sidecarLogTrimEvery < 10 || sidecarLogTrimEvery > 900 {
		t.Errorf("sidecarLogTrimEvery = %ds, outside [10s, 900s]; the cap is only as tight "+
			"as the interval that enforces it", sidecarLogTrimEvery)
	}
}
