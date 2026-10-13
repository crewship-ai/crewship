package hooks

import (
	"os"
	"sync/atomic"
)

// Lifecycle hooks are PAUSED — work in progress, not offered to anyone.
//
// The registry, the handlers and every dispatch site stay in the tree, but
// nothing runs: Dispatch returns before reading hooks_config, the
// /api/v1/hooks routes answer 503 (internal/api/hooks_handler.go), the
// `crewship hooks` command is hidden, and Settings no longer links to the
// page. Rows already in hooks_config are kept untouched, so resuming is a
// switch, not a migration.
//
// Why: what exists today needs a developer to be useful — a webhook receives
// the raw event JSON, which Slack, Teams or PagerDuty will not accept without
// a relay in between; the subagent handler was never wired; shell is
// owner-only. Until hooks can reach those destinations directly, offering
// them reads as a feature nobody can use.
//
// CREWSHIP_HOOKS_ENABLED=1 turns them back on for one server process, for
// development of the feature. Tests that exercise hooks call
// EnableForTests from an init in a _test.go file of their package.
const EnabledEnv = "CREWSHIP_HOOKS_ENABLED"

// PausedMessage is what the API and the CLI say instead of acting.
const PausedMessage = "Lifecycle hooks are paused: the feature is unfinished and does not run. Existing hooks are kept but do not fire."

var enabled atomic.Bool

func init() {
	enabled.Store(os.Getenv(EnabledEnv) == "1")
}

// Enabled reports whether lifecycle hooks run in this process.
func Enabled() bool { return enabled.Load() }

// EnableForTests turns hooks on for the whole test binary of the calling
// package. Never call it from non-test code.
func EnableForTests() { enabled.Store(true) }

// SetEnabledForTest flips the switch for one test and restores it after.
// Never call it from non-test code.
func SetEnabledForTest(t interface{ Cleanup(func()) }, on bool) {
	prev := enabled.Load()
	enabled.Store(on)
	t.Cleanup(func() { enabled.Store(prev) })
}
