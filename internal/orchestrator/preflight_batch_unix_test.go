//go:build !windows

package orchestrator

// The merged preflight script, run through the real /bin/sh (#1646).
//
// buildPreflightScript is a shell program, and asserting on its TEXT proves
// nothing about how a shell executes it: the stdin isolation, the failure
// accumulator and the per-step subshell are all behaviours, not substrings.
// These tests run the generated script exactly the way the container does —
// `sh` with the script on stdin — and assert what came out.
//
// Behind //go:build !windows rather than a runtime.GOOS t.Skip: a skip reports
// the same "ok" as a pass, which is what scripts/skip-budget.sh exists to stop.

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPreflightScript executes the merged script the way the provider does —
// `sh` reading its commands from stdin, no argv — and returns combined output
// plus the exit code.
func runPreflightScript(t *testing.T, dir string, steps []preflightStep) (string, int) {
	t.Helper()
	cmd := exec.Command("sh")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(buildPreflightScript(steps))
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the merged script: %v", err)
	}
	return string(out), code
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if b, err := readFileIfExists(path); err == nil {
		t.Errorf("%s should not exist, contains %q", path, b)
	}
}

func readFileIfExists(path string) (string, error) {
	b, err := exec.Command("cat", path).Output()
	return string(b), err
}

// A step that reads stdin must not be able to consume the rest of the script.
// The script IS stdin; without the per-step `</dev/null` redirect a stray
// `cat` swallows every command sh has not parsed yet, and the run silently
// loses its remaining preflight — no error, no failure line, just missing
// files.
func TestBuildPreflightScript_StepReadingStdinCannotEatTheScript(t *testing.T) {
	dir := t.TempDir()
	out, code := runPreflightScript(t, dir, []preflightStep{
		{name: "first", script: "echo one > one.txt"},
		{name: "greedy", script: "cat > swallowed.txt"},
		{name: "after-greedy", script: "echo three > three.txt"},
		{name: "last", script: "echo four > four.txt"},
	})

	for _, f := range []string{"one.txt", "three.txt", "four.txt"} {
		if _, err := readFileIfExists(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s missing — a step that reads stdin ate the rest of the merged "+
				"script (output: %q, exit %d)", f, out, code)
		}
	}
	swallowed, err := readFileIfExists(filepath.Join(dir, "swallowed.txt"))
	if err != nil {
		t.Fatalf("the greedy step did not run at all: %v", err)
	}
	if swallowed != "" {
		t.Errorf("the greedy step read %q from stdin; every step must see EOF immediately", swallowed)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 (no step failed)", code)
	}
}

// A failing step must be NAMED, and must not abort the steps after it. The
// pre-merge form ran every step as its own exec, so a failure never
// short-circuited its successors; callers rely on that (a failed memory
// migration must not cost the run its credential files).
func TestBuildPreflightScript_FailingStepIsNamedAndLaterStepsStillRun(t *testing.T) {
	dir := t.TempDir()
	out, code := runPreflightScript(t, dir, []preflightStep{
		{name: "before", script: "echo a > before.txt"},
		{name: "credentials", script: "echo 'permission denied' >&2; exit 13"},
		{name: "after", script: "echo c > after.txt"},
	})

	if code == 0 {
		t.Errorf("exit = 0 despite a failed step; the merged script must exit non-zero")
	}
	failed := parsePreflightFailures(out)
	if len(failed) != 1 || failed[0] != "credentials" {
		t.Errorf("failure line named %v, want exactly [credentials] — a merged script "+
			"that cannot say WHICH part failed makes a broken crew materially harder "+
			"to debug than the one-exec-per-step form it replaced. Output: %q", failed, out)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("the failing step's own diagnostics were lost; output: %q", out)
	}
	if !strings.Contains(out, preflightStepMarker+"credentials") {
		t.Errorf("the transcript does not announce the credentials step; output: %q", out)
	}
	for _, f := range []string{"before.txt", "after.txt"} {
		if _, err := readFileIfExists(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s missing — a failing step short-circuited its neighbours, which "+
				"the pre-merge one-exec-per-step form never did", f)
		}
	}
}

// Every step succeeding must produce NO failure line, or the Go side reports a
// healthy preflight as broken on every run.
func TestBuildPreflightScript_AllStepsSucceedingEmitsNoFailureLine(t *testing.T) {
	dir := t.TempDir()
	out, code := runPreflightScript(t, dir, []preflightStep{
		{name: "a", script: "echo 1 > a.txt"},
		{name: "b", script: "true"},
	})
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if failed := parsePreflightFailures(out); failed != nil {
		t.Errorf("healthy run reported failures %v; output: %q", failed, out)
	}
}

// A step's working directory — and anything else it does to its shell — must
// not leak into the next step. The steps used to be separate execs, so each
// one started from the container's own cwd.
func TestBuildPreflightScript_WorkingDirAndStateAreScopedToTheirStep(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := exec.Command("mkdir", "-p", sub).Run(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, code := runPreflightScript(t, dir, []preflightStep{
		{name: "in-subdir", workingDir: sub, script: "echo x > here.txt"},
		{name: "back-at-root", script: "echo y > here.txt"},
	})
	if code != 0 {
		t.Fatalf("exit = %d, output %q", code, out)
	}
	if _, err := readFileIfExists(filepath.Join(sub, "here.txt")); err != nil {
		t.Error("the workingDir step did not write inside its directory")
	}
	root, err := readFileIfExists(filepath.Join(dir, "here.txt"))
	if err != nil {
		t.Fatal("the following step wrote nowhere the caller expected — a step's `cd` leaked")
	}
	if strings.TrimSpace(root) != "y" {
		t.Errorf("root here.txt = %q, want y", strings.TrimSpace(root))
	}
}

// A step whose workingDir does not exist must fail as ITSELF, not silently
// write its files into whatever directory the shell happened to be in.
func TestBuildPreflightScript_MissingWorkingDirFailsTheStepNotItsNeighbours(t *testing.T) {
	dir := t.TempDir()
	out, code := runPreflightScript(t, dir, []preflightStep{
		{name: "orphaned", workingDir: filepath.Join(dir, "nope"), script: "echo x > stray.txt"},
		{name: "healthy", script: "echo y > ok.txt"},
	})
	if code == 0 {
		t.Error("a step whose workingDir is missing must fail")
	}
	if failed := parsePreflightFailures(out); len(failed) != 1 || failed[0] != "orphaned" {
		t.Errorf("failure line = %v, want [orphaned]; output %q", failed, out)
	}
	mustNotExist(t, filepath.Join(dir, "stray.txt"))
	if _, err := readFileIfExists(filepath.Join(dir, "ok.txt")); err != nil {
		t.Error("the healthy step did not run")
	}
}

// Step names carry file paths and skill slugs into a printf argument and into
// a double-quoted shell assignment. Anything a caller can influence has to be
// inert there.
//
// The hostile names go in through preflightBatch.add — the real path a
// relPath or a skill slug takes — rather than being sanitised by the test
// first. Sanitising in the test would leave the WIRING untested: deleting the
// sanitiser call from add() kept an earlier version of this test green while
// the names went through raw.
func TestBuildPreflightScript_HostileStepNameCannotExecute(t *testing.T) {
	dir := t.TempDir()
	hostile := []string{
		"file:$(touch pwned-a)",
		"file:'; touch pwned-b; '",
		"file:`touch pwned-c`",
		"file:${IFS}touch${IFS}pwned-d",
		"file:\ntouch pwned-e\n",
	}
	b := newPreflightBatch(nil, "ctr-1", "1001:1001", nil)
	for _, name := range hostile {
		b.add(name, "", "true")
	}
	out, code := runPreflightScript(t, dir, b.steps)
	if code != 0 {
		t.Errorf("exit = %d, output %q", code, out)
	}
	for _, f := range []string{"pwned-a", "pwned-b", "pwned-c", "pwned-d", "pwned-e"} {
		mustNotExist(t, filepath.Join(dir, f))
	}
}
