package pipeline

import "database/sql"

// ExecutorDeps bundles every dependency a production Executor needs so
// that ALL construction sites share one wiring path (NewWiredExecutor):
//
//   - the HTTP handler (internal/api/pipelines.go newExecutor)
//   - the boot-time resume scan (cmd/crewship/cmd_start.go)
//   - the cron scheduler executor (cmd/crewship/cmd_start.go)
//   - the pending-run dispatcher (shares the scheduler's executor)
//
// History: these sites used to hand-assemble the executor with
// inconsistent With* subsets, so capabilities proven on the HTTP path
// silently failed on the unattended paths (cron-fired wait:approval hit
// the nil-store 60s fallback; resumed code steps failed "no CodeRunner
// wired"; resumed runs dropped step overrides and failed wait:event).
// Any per-site difference (e.g. WithResumeCutoff on the boot scan) must
// be an explicit chained call at the call site — never an omission.
//
// Nil semantics match the With* builders: a nil optional dep leaves the
// corresponding capability in its documented degraded mode. Fields
// typed as interfaces (Waitpoints, WS, CodeRunner) must be passed as
// untyped nil or a non-nil implementation — beware typed-nil pointers.
type ExecutorDeps struct {
	// Required core (same contract as NewExecutor). Emitter may be nil —
	// it falls back to the no-op emitter.
	Store    *Store
	Resolver *Resolver
	Runner   AgentRunner
	Emitter  Emitter

	// DB, when non-nil, derives the idempotency + step-override stores.
	// Both are thin, goroutine-free DB wrappers, so constructing them
	// here (rather than accepting instances) makes them impossible to
	// forget at a call site — the exact omission class this factory
	// exists to close.
	DB *sql.DB

	// Optional capabilities. Nil → documented degraded behaviour (see
	// the field docs on Executor / the With* builders).
	Waitpoints WaitpointStore  // nil → wait:approval falls back to in-memory 60s timeout
	WS         WSBroadcaster   // nil → no live event push; journal poll only
	Runs       *RunRegistry    // nil → no cancel + no concurrency_key gate
	RunStore   *RunStore       // nil → no pipeline_runs persistence / boot recovery
	CodeRunner CodeRunner      // nil → type:code steps fail closed with a wiring hint
	Signals    *SignalRegistry // nil → wait:event fails closed
}

// NewWiredExecutor builds a fully-wired Executor from the dependency
// bundle. It is the single production construction path; adding a new
// executor capability means adding a field here (and wiring it below),
// at which point every call site picks it up at once. The construction
// parity test in executor_factory_test.go sweeps the Executor's fields
// so a capability added to the struct but not to this factory fails CI.
func NewWiredExecutor(d ExecutorDeps) *Executor {
	exec := NewExecutor(d.Store, d.Resolver, d.Runner, d.Emitter)
	if d.Waitpoints != nil {
		exec = exec.WithWaitpointStore(d.Waitpoints)
	}
	if d.WS != nil {
		exec = exec.WithWSBroadcaster(d.WS)
	}
	if d.Runs != nil {
		exec = exec.WithRunRegistry(d.Runs)
	}
	if d.DB != nil {
		exec = exec.WithIdempotencyStore(NewIdempotencyStore(d.DB))
		exec = exec.WithStepOverrides(NewStepOverrideStore(d.DB))
	}
	if d.RunStore != nil {
		exec = exec.WithRunStore(d.RunStore)
	}
	if d.CodeRunner != nil {
		exec = exec.WithCodeRunner(d.CodeRunner)
	}
	if d.Signals != nil {
		exec = exec.WithSignalRegistry(d.Signals)
	}
	return exec
}
