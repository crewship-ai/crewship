package testutil

// migrateddb_inherit.go lets a test that re-executes its own test binary (to
// get a process with different global state — a non-UTC time.Local, a crash
// to survive) hand the child the template the parent already built.
//
// Without it the child builds its own: the full migration chain, ~55 s under
// -race on crewship-dev, paid again for one test. internal/api's
// TestRecurringIssueCreate_NextRunIsUTC spent 43.8 s of its 43.8 s doing that
// in CI.
//
// Equivalence: the child is the same executable, so its migration registry is
// byte-for-byte the parent's and the template the parent built is the one the
// child would build. inheritedTemplate refuses the variable unless the
// executable it names is the running one (os.SameFile), so a value left in a
// shell, or passed to a different binary, falls back to building.

import (
	"os"
	"testing"
)

const (
	inheritTemplateEnv    = "CREWSHIP_TESTUTIL_MIGRATED_TEMPLATE"
	inheritTemplateExeEnv = "CREWSHIP_TESTUTIL_MIGRATED_TEMPLATE_EXE"
)

// MigratedTemplateEnv returns environment entries that let a child process of
// this same test binary reuse this process's migrated template. Append them to
// the child's cmd.Env. Builds the template first if this process has not yet.
func MigratedTemplateEnv(t testing.TB) []string {
	t.Helper()
	path := MigratedTemplatePath(t)
	exe, err := os.Executable()
	if err != nil {
		// No way to prove the child is the same binary: let it build its own.
		return nil
	}
	return []string{inheritTemplateEnv + "=" + path, inheritTemplateExeEnv + "=" + exe}
}

// inheritedTemplate reports the parent's template when this process is the
// binary that built it.
func inheritedTemplate() (string, bool) {
	path, exe := os.Getenv(inheritTemplateEnv), os.Getenv(inheritTemplateExeEnv)
	if path == "" || exe == "" {
		return "", false
	}
	self, err := os.Executable()
	if err != nil {
		return "", false
	}
	want, err := os.Stat(exe)
	if err != nil {
		return "", false
	}
	got, err := os.Stat(self)
	if err != nil || !os.SameFile(want, got) {
		return "", false
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return path, true
}
