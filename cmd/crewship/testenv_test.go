package main

import (
	"os"
	"strings"
	"testing"
)

// scrubAmbientCrewshipEnv removes every CREWSHIP_* variable from the test
// process environment.
//
// Why: the CLI resolves its target from env *before* the config file
// (`internal/cli/config.go` ResolveServer, `internal/cli/profiles.go`
// ActiveProfileName/EffectiveServer). A developer whose shell exports
// CREWSHIP_PROFILE / CREWSHIP_SERVER for their own dev instance — which the
// multi-clone workflow encourages — therefore makes ~180 tests in this
// package assert against their live instance instead of the stub server or
// temp config the test just wrote. The failures look like unrelated logic
// bugs, so they cost real debugging time (#1305).
//
// The scrub is wholesale rather than a fixed list: any new CREWSHIP_* knob
// is hermetic by default, and tests that need one set it themselves with
// t.Setenv after TestMain has run.
//
// DATABASE_URL is scrubbed alongside them despite the different prefix. It
// steers resolveLocalDBTarget — and, since #2109, openLocalDBReadOnly and so
// every read-only doctor probe — ahead of CREWSHIP_DATA_DIR, so a shell that
// exports it points the same ~180 tests at a database the test did not write.
// It is the same failure as #1305 through a variable the prefix rule misses.
func scrubAmbientCrewshipEnv() {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && (strings.HasPrefix(name, "CREWSHIP_") || name == "DATABASE_URL") {
			os.Unsetenv(name)
		}
	}
}

// TestNoAmbientCrewshipEnv is the tripwire for the scrub above: if TestMain
// ever stops calling it, this fails on any developer machine that exports
// CREWSHIP_* — the exact configuration the scrub exists to survive.
func TestNoAmbientCrewshipEnv(t *testing.T) {
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && (strings.HasPrefix(name, "CREWSHIP_") || name == "DATABASE_URL") {
			t.Errorf("ambient %s leaked into the test process; TestMain must scrub CREWSHIP_* and DATABASE_URL before m.Run", name)
		}
	}
}
