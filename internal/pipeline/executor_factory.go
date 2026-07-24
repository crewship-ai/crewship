package pipeline

import (
	"database/sql"
	"log/slog"
	"sync"

	"github.com/crewship-ai/crewship/internal/llm"
)

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

	// DB, when non-nil, derives the idempotency + step-override stores
	// AND the http-step security perimeter: the crew network-policy
	// egress gate (NewCrewNetworkPolicyGate) and the workspace vault
	// credential resolver (NewVaultCredentialResolver). All four are
	// thin, goroutine-free DB wrappers, so constructing them here
	// (rather than accepting instances) makes them impossible to
	// forget at a call site — the exact omission class this factory
	// exists to close. The two security wires earned their place the
	// hard way: WithEgressGate / WithCredentialResolver existed for
	// months with ZERO production callers, so http steps ran with
	// allow-all crew egress and could never receive vault credentials
	// while the DSL kept validating egress_targets / credential_ref.
	DB *sql.DB

	// Optional capabilities. Nil → documented degraded behaviour (see
	// the field docs on Executor / the With* builders).
	Waitpoints WaitpointStore  // nil → wait:approval falls back to in-memory 60s timeout
	WS         WSBroadcaster   // nil → no live event push; journal poll only
	Runs       *RunRegistry    // nil → no cancel + no concurrency_key gate
	RunStore   *RunStore       // nil → no pipeline_runs persistence / boot recovery
	CodeRunner CodeRunner      // nil → type:code steps fail closed with a wiring hint
	Signals    *SignalRegistry // nil → wait:event fails closed

	// ScriptRunner execs type:script steps (bundled scripts) in the crew
	// container. nil → script steps fail closed with a wiring hint. In
	// production this is the same OrchestratorRunner passed as Runner
	// (it implements both AgentRunner and ScriptRunner).
	ScriptRunner ScriptRunner

	// RunVerdictProvider/RunVerdictModel back the post-run outcome
	// verdict (#1403): a pre-resolved LLM provider + model, built once
	// at boot from the run_summary aux slot (mirror
	// internal/api/router_internal.go's wiring for ad-hoc agent runs —
	// same feature_flags row gates both). nil provider (or nil DB) →
	// no verdict hook installed, matching every other optional
	// capability's degraded-mode contract.
	RunVerdictProvider llm.Provider
	RunVerdictModel    string

	// VerdictWG, when set, is a shared WaitGroup the async verdict
	// goroutines register on instead of the executor's own — so a
	// long-lived owner (PipelineHandler) can drain in-flight verdicts
	// across every ephemeral executor it builds before the journal writer
	// closes at shutdown (#1403). nil → executor-local group (fine for
	// tests and one-shot executors).
	VerdictWG *sync.WaitGroup
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
		// wait:event durability (#1409) — same thin, goroutine-free DB
		// wrapper shape as the two stores above, so every production
		// executor gets it automatically: forgetting it at a call site
		// is the exact class of omission this factory exists to close.
		exec = exec.WithSignalWaitStore(NewSQLSignalWaitStore(d.DB))
		// cross-run routine state (#1420) — {{ routine.state.* }} reads +
		// state_write persistence, same thin DB-wrapper shape so every
		// production executor (HTTP, boot resume, cron, dispatcher) shares
		// one state bucket per (pipeline, schedule).
		exec = exec.WithStateStore(NewRoutineStateStore(d.DB))
		// http-step security perimeter — both read the same DB the
		// rest of the run uses, so any production executor (HTTP
		// handler, boot resume, cron scheduler, pending dispatcher)
		// enforces the crew network policy and resolves vault
		// credentials identically. Back-compat contract lives on the
		// implementations: crews without a policy stay 'free', and a
		// routine with no egress_targets stays unrestricted at the
		// routine layer.
		exec = exec.WithEgressGate(NewCrewNetworkPolicyGate(d.DB))
		exec = exec.WithCredentialResolver(NewVaultCredentialResolver(d.DB))
		// notify-step inbox sink — same DB the run uses, so every
		// production executor (HTTP handler, boot resume, cron scheduler,
		// pending dispatcher) can deliver routine notifications.
		exec = exec.WithInboxNotifier(&sqlInboxNotifier{db: d.DB, logger: slog.Default()})
		// notify-step membership guard (user:<id> → workspace fallback on
		// a non-member id, instead of a silent black hole).
		exec = exec.WithMemberChecker(NewWorkspaceMemberChecker(d.DB))
		// notify-step crew audience (crew:<slug> → the crew's human members,
		// scoped to this workspace).
		exec = exec.WithCrewAudienceResolver(NewCrewAudienceResolver(d.DB))
		// notify-step per-recipient anti-spam soft cap — bounds how many
		// routine notices one run can pile on a single inbox (perRunNotifyCap).
		exec = exec.WithNoticeCounter(NewRunNoticeCounter(d.DB))
	}
	if d.RunStore != nil {
		exec = exec.WithRunStore(d.RunStore)
	}
	if d.CodeRunner != nil {
		exec = exec.WithCodeRunner(d.CodeRunner)
	}
	if d.ScriptRunner != nil {
		exec = exec.WithScriptRunner(d.ScriptRunner)
	}
	if d.Signals != nil {
		exec = exec.WithSignalRegistry(d.Signals)
	}
	// Post-run outcome verdict (#1403) — needs both a DB (feature flag
	// + journal entries) and a pre-resolved LLM provider (built once at
	// boot from the run_summary aux slot). Either missing → the
	// capability stays off, matching every other optional dependency's
	// degraded-mode contract.
	if d.DB != nil && d.RunVerdictProvider != nil {
		exec = exec.WithRunVerdict(newRunVerdictHook(d.DB, exec.emitter, d.RunVerdictProvider, d.RunVerdictModel, slog.Default()))
	}
	if d.VerdictWG != nil {
		exec = exec.WithSharedVerdictWaitGroup(d.VerdictWG)
	}
	return exec
}
