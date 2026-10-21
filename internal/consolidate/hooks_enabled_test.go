package consolidate

import "github.com/crewship-ai/crewship/internal/hooks"

// Lifecycle hooks are paused in production (internal/hooks/paused.go); this
// package's tests exercise them, so its test binary runs with them on.
func init() { hooks.EnableForTests() }
