package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The rule this file enforces: a test that mutates process-wide CLI
// state must be SERIAL.
//
// resetCLIState restores cliCfg, the flag* vars and cli.Bold/Reset/… from
// t.Cleanup, and guardCLIState registers it. That is safe for a serial
// test — Go runs a serial test's body and its cleanups during the
// sequential pass, while every parallel test is still parked — and it is
// unsafe for a parallel one, whose cleanup fires at an arbitrary point
// relative to every other parallel test in the package.
//
// #1610 is what that costs: shellPromptString reads cli.Bold to build the
// prompt, resetCLIState writes it from a neighbour's cleanup, and the
// race detector printed both stacks. Ten-plus tests failed as a
// consequence of two racing pairs.
//
// Why a source-level guard rather than an assertion inside
// guardCLIState: testing.T does not expose whether a test called
// t.Parallel(), so there is nothing to assert against at runtime short
// of a side-effecting trick (t.Setenv panics for parallel tests — but
// it also mutates the environment, which is a strange thing for a guard
// to do). The pairing is perfectly visible in source, so it is checked
// there.
//
// The cost of the rule is one test: at the time it was written, exactly
// ONE of the package's 189 parallel tests reached a state-mutating
// helper. That measurement is why "make the writers serial" was the fix
// rather than the larger "stop mutating globals per test" — the latter
// remains the better end state, and this guard does not block it.

// cliStateHelperRoots are the functions that mutate process-wide CLI
// state, directly or by registering guardCLIState's cleanup. Callers of
// these must not be parallel.
//
// Listed rather than derived by parsing: a call-graph walk over ~200
// test files needs a Go parser to be trustworthy, and a guard nobody
// wants to debug beats a clever one. A helper missing from this list
// weakens the check silently, which is why the test also asserts that
// every name here still exists.
var cliStateHelperRoots = []string{
	"guardCLIState",
	"resetCLIState",
	"saveCLIState",
	"covStub",
	"covSetup",
	"covSetupCli4",
	"covSetupCli5",
	"covSetupCli6",
	"covSetupCli8",
	"covSetupCli10",
	"covSetupDead",
	"covSetupRunSeed",
	"covSaveState",
	"setStubCLI",
	"covResetFlags",
	"covResetFlagsCli6",
	"covSetFlag",
	"covSetFlags",
	"covSetFlagCli5",
	"covSetFlagCli6",
	"covSetFlagCli8",
	"covSetFlagCli9",
	"covSetFlagsCli4",
	"setFlagCov",
	"setFlagCovCli10",
	"setFormatCov",
	"covSetFormat",
	"covCaptureAll",
	"covCaptureFD",
	"covCaptureStdout",
	"covCaptureStderr",
	"covCaptureStdoutCli3",
	"covCaptureStdoutCli4",
	"covCaptureStdoutCli5",
	"covCaptureStdoutCli6",
	"covCaptureStdoutCli7",
	"covCaptureStdoutCli8",
	"covCaptureStdoutCli9",
	"covCaptureStderrCli6",
	"covCaptureStderrCli9",
	"captureStdoutCov",
	"captureStdoutCovCli2",
	"captureStdoutCovCli10",
	"covResetForecastFlags",
	"covResetCrewConfigFlags",
	"covResetMissionCreateFlags",
	"covResetRunCmdFlags",
	"covResetAgentMCPAddFlags",
	"covResetAgentMCPUpdateFlags",
	"covResetBackupCreateFlags",
	"covResetBackupRestoreFlags",
}

var (
	funcSplit    = regexp.MustCompile(`\nfunc\s`)
	topParallel  = regexp.MustCompile(`(?m)^\tt\.Parallel\(\)`)
	funcNameHead = regexp.MustCompile(`^([A-Za-z0-9_]+)`)
)

// TestCLIStateGuard_NoParallelWriter fails when a top-level test both
// calls t.Parallel() and reaches a CLI-state-mutating helper.
func TestCLIStateGuard_NoParallelWriter(t *testing.T) {
	calls := regexp.MustCompile(`\b(` + strings.Join(cliStateHelperRoots, "|") + `)\s*\(`)

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found — the guard would pass vacuously")
	}

	var offenders []string
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, fn := range funcSplit.Split(string(src), -1)[1:] {
			name := funcNameHead.FindString(fn)
			if !strings.HasPrefix(name, "Test") {
				continue
			}
			if !topParallel.MatchString(fn) {
				continue
			}
			if m := calls.FindStringSubmatch(fn); m != nil {
				offenders = append(offenders, file+":"+name+" -> "+m[1])
			}
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("parallel tests that mutate process-wide CLI state:\n  %s\n\n"+
			"resetCLIState runs from t.Cleanup and rewrites cliCfg, the flag* vars and "+
			"cli.Bold/Reset/…. For a parallel test that cleanup fires at an arbitrary point "+
			"relative to every other parallel test, which is a real data race against any of "+
			"them reading those vars (#1610: shellPromptString reads cli.Bold).\n"+
			"Drop t.Parallel() from the test above — a serial test's cleanup runs while every "+
			"parallel test is still parked, so it cannot race one.",
			strings.Join(offenders, "\n  "))
	}
}

// TestCLIStateGuard_HelperListIsCurrent keeps the list above honest. A
// renamed or deleted helper would otherwise leave a dead entry, and the
// guard would quietly cover less than its comment claims — the same
// "green without checking anything" failure the list exists to prevent.
func TestCLIStateGuard_HelperListIsCurrent(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	var all strings.Builder
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		all.Write(src)
	}
	src := all.String()

	for _, name := range cliStateHelperRoots {
		if !regexp.MustCompile(`\nfunc\s+` + regexp.QuoteMeta(name) + `\s*\(`).MatchString(src) {
			t.Errorf("cliStateHelperRoots names %q, which no longer exists — "+
				"remove it, or rename the entry to match the helper it replaced", name)
		}
	}
}
