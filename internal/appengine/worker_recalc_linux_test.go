//go:build linux

package appengine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// scriptEngine replaces a fake CLI with a complete script that dispatches on
// its subcommand, keeping the engine's scratch and read-only roots.
func scriptEngine(t *testing.T, engine WorkerEngine, script string) WorkerEngine {
	t.Helper()
	if err := os.WriteFile(engine.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	engine.digest = hex.EncodeToString(sum[:])
	return engine
}

// runSteps copies the input for `run`; `--cmd` shifts the output argument.
const runSteps = `run)
  if [ "$4" = "--cmd" ]; then dst="$7"; else dst="$5"; fi
  cp "$3" "$dst" ;;`

// The recalculation check is an engine execution like any other: confined by
// Landlock and seccomp, under the engine's resource limits, in its own
// directory, with only its report on standard output.
func TestRecalcCheckRunsInsideTheSandbox(t *testing.T) {
	engine := fakeCLI(t, "")
	engine = scriptEngine(t, engine, `#!/bin/sh
set -u
case "$1" in
`+runSteps+`
check)
  [ "$2" = candidate.json ] && [ -s candidate.json ] || exit 79
  if cat /proc/self/status >/dev/null 2>&1; then exit 80; fi
  if setsid true </dev/null >/dev/null 2>&1; then exit 81; fi
  if ls .. >/dev/null 2>&1; then exit 82; fi
  [ "$(ulimit -f)" = 8192 ] || exit 83
  printf '{"schema":"gridcraft-check/1","files":[{"opened":true,"error":null,"calc":{"formulaCells":0,"checked":0,"mismatched":0,"noOracle":0,"volatile":0,"staleCache":false}}]}'
  ;;
*) exit 78 ;;
esac
`)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	result, err := engine.Run(t.Context(), blankWorkbook(t), Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
	if err != nil {
		t.Fatalf("check outside the sandbox or without its report: %v", err)
	}
	if result.Recalc == nil || result.Recalc.Mismatched != 0 {
		t.Fatalf("recalc %+v", result.Recalc)
	}
	sum := sha256.Sum256(result.Result)
	if result.Recalc.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("report not bound to the returned bytes")
	}
	if escapes.Load() != 0 {
		t.Fatalf("hook called %d times", escapes.Load())
	}
	if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "*")); len(files) != 0 {
		t.Fatalf("scratch survived: %v", files)
	}
}

// A compromised cell.set leaves a process behind that rewrites the file the
// check reads into an honest workbook, so forged cached values would pass.
// It cannot start a session, so it dies with its execution's group, and it
// could not write the check's directory anyway. The real check then sees the
// forged values.
func TestRecalcCheckCannotBeRewrittenBySurvivingProcess(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY to the pinned CLI to execute the real check")
	}
	testdata, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	engine := fakeCLI(t, "")
	engine.ReadOnly = append(engine.ReadOnly, testdata, filepath.Dir(real))
	engine = scriptEngine(t, engine, fmt.Sprintf(`#!/bin/sh
set -u
forged=%[1]q/recalc-forged.json
honest=%[1]q/recalc-honest.json
scratch=$(dirname "$TMPDIR")
case "$1" in
run)
  if [ "$4" != "--cmd" ]; then cp "$3" "$5"; exit 0; fi
  cp "$forged" "$7"
  rewrite='end=$(($(date +%%s)+15)); while [ $(date +%%s) -lt $end ]; do for f in "$0"/check-*/candidate.json; do [ -f "$f" ] && cp "$1" "$f" 2>/dev/null; done; sleep 0.002; done'
  setsid sh -c "$rewrite" "$scratch" "$honest" </dev/null >/dev/null 2>&1 &
  ( sh -c "$rewrite" "$scratch" "$honest" & ) </dev/null >/dev/null 2>&1
  sleep 0.05 ;;
check) exec %[2]q "$@" ;;
*) exit 78 ;;
esac
`, testdata, real))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	raw, err := os.ReadFile(filepath.Join(testdata, "recalc-honest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		result, err := engine.Run(t.Context(), raw, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "A1", Input: "5"})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if result.Recalc == nil || result.Recalc.Mismatched == 0 || result.Recalc.verified() {
			t.Fatalf("run %d: forged cached values passed the check: %+v", i, result.Recalc)
		}
	}
	if escapes.Load() != 0 {
		t.Fatalf("an in-group leftover is not an escape; hook called %d times", escapes.Load())
	}
}

// The check is refused like any engine execution when its output is not a
// usable report.
func TestRecalcCheckRejectsMissingReport(t *testing.T) {
	engine := fakeCLI(t, "")
	engine = scriptEngine(t, engine, `#!/bin/sh
set -u
case "$1" in
`+runSteps+`
check) exit 1 ;;
*) exit 78 ;;
esac
`)
	_, err := engine.Run(t.Context(), blankWorkbook(t), Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
	if !errors.Is(err, ErrCandidate) {
		t.Fatalf("missing report must reject the candidate, got %v", err)
	}
}
