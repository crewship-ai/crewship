// Package pipeline implements Crewship Routines — workspace-scoped
// declarative AI workflow recipes. The user-facing label is "Routine"
// across the web UI, CLI, agent system prompt, and marketing surfaces;
// internal identifiers stay "pipeline" / "Pipeline*" for backwards
// compat (this package, the database table, the HTTP route paths).
// See docs/guides/routines-migration.mdx and PIPELINES.md §17 for the
// rename rationale.
//
// # Architecture overview
//
// Three authoring paths converge on the same Store.Save:
//
//  1. Agent — sidecar's POST /pipelines/save (port 9119) injects the
//     author crew identity from the IPC config and forwards to the
//     main API's /api/v1/internal/pipelines/save (X-Internal-Token).
//  2. UI — POST /api/v1/workspaces/{ws}/pipelines/save (JWT, MANAGER+).
//     Author identity is taken from the JWT user (not body-trusted).
//  3. CLI — `crewship routine save` posts to the same internal API
//     using a CLI token; identity = the user behind the token.
//
// The test_run gate runs before save: callers must run `/test_run`
// first or pass `skip_test_gate` (OWNER/ADMIN). The HMAC `save_token`
// (see pipelines_save_token.go in internal/api) is the trustworthy
// proof that "this user just ran test_run for this exact DSL"; when
// present and valid, it supersedes body-trust on last_test_run_at.
//
// # Step types
//
// Six step kinds in MVP:
//   - StepAgentRun     — invoke an agent via CLI adapter (Claude, Gemini, etc.)
//   - StepCallPipeline — recurse into another routine (cycle-detected at save)
//   - StepHTTP         — outbound HTTP request with egress allowlist + redirect guard
//   - StepCode         — Python/Go/Bash sandbox container execution
//   - StepWait         — pause primitive (approval / datetime / event kinds)
//   - StepTransform    — pure-Go data reshaping (jq-flavoured subset)
//
// Each step can carry validation gates (JSON Schema subset + Crewship
// extensions), a conditional `if`, retry/backoff policy, and DAG
// dependencies via Needs[]. Independent steps with disjoint Needs[]
// execute in parallel goroutines (one wave per ready set).
//
// # Two-tier execution
//
// The economic value-prop: an authoring-tier model (Opus) writes the
// routine; an executor-tier model (Haiku, Ollama) runs each invocation.
// Workspace.execution_tiers_json maps complexity classes to (adapter,
// model) pairs. Per-step Complexity annotation drives the resolver;
// step.ModelOverride is the explicit pin that wins. The CLI adapter
// receives the tier-resolved model via runner_orchestrator.go's
// override (see runner_orchestrator.go RunStep step 6).
//
// # Triggers
//
// Beyond manual invocation, two trigger primitives:
//
//  1. Schedules (schedules.go, migration v80) — cron-driven, in-process
//     scheduler ticks every 30s. Single-instance only; no leader
//     election yet (Foundation PRD scope).
//  2. Webhooks (webhooks.go, migration v82) — token-addressed event
//     triggers with optional HMAC signing + per-token rate limiting.
//     Idempotency keys (idempotency.go, migration v81) deduplicate
//     redelivery.
//
// # HITL waitpoints
//
// StepWait of kind=approval parks the run goroutine on a DB-backed
// waitpoint (waitpoints.go, migration v79). Operators decide via
// CLI / UI / API; the WaitFor pre-registers the listener channel
// before the decided-state DB check to eliminate the lost-wakeup
// race that earlier had goroutines parked forever after a fast-path
// CompleteApproval. RecoverPending sweeps timed-out entries at boot;
// runs parked on a wait step are then re-attached to their original
// pending token by the boot resume scan (resume.go), so approvals
// stay answerable across restarts.
//
// # Cross-references
//
//   - docs/guides/routines.mdx                — user-facing concepts
//   - docs/cli/routine.mdx                    — CLI subcommand reference
//   - schemas/routine.v1.json                 — JSON Schema for IDE support
//   - internal/api/pipelines.go               — HTTP handlers
//   - internal/api/pipelines_save_token.go    — HMAC save_token primitives
//   - internal/sidecar/pipelines.go           — agent-facing endpoints (port 9119)
//   - cmd/crewship/seeddata/routines.go       — 5 starter routines for fresh workspaces
package pipeline
