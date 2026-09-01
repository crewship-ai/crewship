// Package journal is the append-only event stream that backs the Crew Journal
// product. Every observable action in the platform — peer conversations,
// mission changes, keeper decisions, exec/network/file events, LLM calls,
// approvals, checkpoints, hook fires — lands here as an entry. Downstream
// features (Summary, Crow's Nest, Paymaster, Cartographer, Episodic Memory)
// are read-models or side-effects over this one stream.
//
// The package deliberately stays small: types, an Emit API, and a batched
// writer. Read paths live under internal/api/journal_handler.go so the
// package has no dependency on HTTP or the router.
package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// EntryType enumerates every kind of event that can be written. Consumers
// branch on the string value; payload schema is defined per-type in the
// typed helpers further below. New types are free to add as long as the
// string is stable — callers MUST NOT rename existing ones without a
// migration that rewrites historical rows.
//
// After adding, removing or renaming a constant in the block below, run
// `go generate ./internal/journal/...` and commit registry_generated.go —
// AllEntryTypes and Registered() are generated from this exact block (see
// cmd/gen-journal-registry), and registry_generated_test.go fails the build
// if the generated file falls behind this one.
//
//go:generate go run ../../cmd/gen-journal-registry
type EntryType string

const (
	// Communication
	EntryPeerConversation EntryType = "peer.conversation"
	EntryPeerEscalation   EntryType = "peer.escalation"
	EntryMessageBroadcast EntryType = "message.broadcast"
	EntryAgentMentioned   EntryType = "agent.mentioned"

	// EntryConversationCompacted is emitted when an agent's prior session
	// history overflows the context budget and the orchestrator either
	// summarizes the overflow into a compaction block or (no summarizer
	// wired / summarize failed) drops it by truncation. Payload carries
	// session_id, overflow_messages, summarized (bool), summary_bytes —
	// the audit trail for "what fell out of the window this turn".
	EntryConversationCompacted EntryType = "conversation.compacted"

	// Mission / task
	EntryMissionStatus    EntryType = "mission.status_change"
	EntryMissionComment   EntryType = "mission.comment"
	EntryAssignmentCreate EntryType = "assignment.created"
	EntryAssignmentRun    EntryType = "assignment.running"
	EntryAssignmentDone   EntryType = "assignment.completed"
	EntryAssignmentFail   EntryType = "assignment.failed"
	EntryCrewAction       EntryType = "crew.action"
	EntryTaskDelegated    EntryType = "task.delegated"

	// Runs — one trace per agent execution. trace_id == run.id; spans
	// (exec/network/llm/...) belonging to the run carry the same trace_id.
	EntryRunStarted   EntryType = "run.started"
	EntryRunCompleted EntryType = "run.completed"
	EntryRunFailed    EntryType = "run.failed"
	EntryRunCancelled EntryType = "run.cancelled"
	EntryRunTimeout   EntryType = "run.timeout"

	// Security
	EntryKeeperRequest     EntryType = "keeper.request"
	EntryKeeperDecision    EntryType = "keeper.decision"
	EntryGuardrailInput    EntryType = "guardrail.input_blocked"
	EntryGuardrailOutput   EntryType = "guardrail.output_blocked"
	EntryApprovalRequest   EntryType = "approval.requested"
	EntryApprovalGranted   EntryType = "approval.granted"
	EntryApprovalDenied    EntryType = "approval.denied"
	EntryApprovalTimeout   EntryType = "approval.timeout"
	EntryApprovalCancelled EntryType = "approval.cancelled"

	// Standing approval grants — a routine's wait:approval gate being
	// disarmed for one step of one routine body
	// (internal/pipeline/trust_grants.go, internal/api/pipeline_trust.go).
	//
	// Namespaced under `approval.` rather than given a `trust.` family of
	// their own, deliberately: these describe the SAME control as
	// approval.granted/denied above, one level up. An operator asking "who
	// decided this gate could pass" needs the one-off decisions and the
	// standing one in a single `approval.*` filter — a separate namespace
	// would answer that question with half the evidence.
	//
	// Both carry: ActorType=user, ActorID=the deciding human, Refs
	// {trust_grant_id, pipeline_id, pipeline_slug, step_id}, Payload
	// {definition_hash, reason, ...}. The definition hash is load-bearing
	// and not decoration — a grant only fires against that exact routine
	// body, so an entry without it cannot say what was trusted.
	//
	// EntryTrustGranted: a gate stopped asking.
	EntryTrustGranted EntryType = "approval.trust_granted"
	// EntryTrustRevoked: a gate starts asking again. Emitted only when the
	// revoke actually flipped a live row — an attempt that changed nothing
	// must not read as a withdrawal that happened.
	EntryTrustRevoked EntryType = "approval.trust_revoked"

	// Credential reveal (PRD-CREDENTIALS-V2-2026 §2.6 L4). These three
	// are the only journal entries written via EmitSync as a
	// PRECONDITION — the action they describe does not happen unless
	// the chained write commits first.
	//
	// EntryCredentialRevealed records a secret being disclosed to a
	// human. Payload: credential_id, credential_name, classification,
	// reason, actor_role, ip. It carries NEITHER the value NOR a hash
	// of it — a hash would turn the tamper-evident audit log into an
	// offline dictionary-attack target for every short secret in the
	// vault, which is the exact opposite of what an audit log is for.
	EntryCredentialRevealed EntryType = "credential.revealed"

	// EntryCredentialRevealPolicy records the workspace-level reveal
	// switch being turned on or off (L1). Turning it ON is the moment a
	// tenant stops being default-deny, so it is an event in its own
	// right, not a settings diff. Payload: enabled, previous.
	EntryCredentialRevealPolicy EntryType = "credential.reveal_policy_changed"

	// EntryCredentialSensitivityLowered records a classification being
	// WEAKENED (L0) — SEALED→RESTRICTED, RESTRICTED→STANDARD. Raising a
	// classification is not journaled: it only ever removes reach, so
	// it needs no ceremony. Lowering hands someone a key that did not
	// exist a moment ago. Payload: credential_id, from, to.
	EntryCredentialSensitivityLowered EntryType = "credential.sensitivity_lowered"

	// Cost
	EntryLLMCall       EntryType = "llm.call"
	EntryLLMCacheHit   EntryType = "llm.cache_hit"
	EntryCostIncurred  EntryType = "cost.incurred"
	EntryBudgetExceed  EntryType = "budget.exceeded"
	EntryBudgetWarning EntryType = "budget.warning"

	// Memory
	EntryMemoryUpdated      EntryType = "memory.updated"
	EntryMemoryConsolidated EntryType = "memory.consolidated"
	EntrySummaryGenerated   EntryType = "summary.generated"
	// EntryMemoryPriorityChanged records a priority marker being raised or
	// lowered on an existing entry (PATCH /api/v1/journal/{id}/priority).
	// Priority is load-bearing — permanent entries are never compacted and
	// pins land in curated pins.md — so the change is journaled rather than
	// applied as a silent UPDATE, and the entry double-checks the
	// journal_entry_priorities ledger: a fabricated row with no matching
	// entry in the keyed chain is detectable by comparing the two.
	//
	// It had been emitted as a bare string literal from
	// internal/api/journal_handler.go since it shipped, which made it the
	// one type in the corpus with no constant anywhere — invisible to any
	// check that reads the Go declarations. Promoted, not dropped: the
	// entry is real, durable and already on disk in every workspace.
	EntryMemoryPriorityChanged EntryType = "memory.priority_changed"
	// EntryMemoryWriteRejected fires when a sidecar /memory/write call
	// is rejected by the scrubber (credential pattern matched in block
	// mode) or by a cap check (file would exceed AGENT.md/CREW.md/pins.md
	// byte ceiling). Payload carries `tier`, `file`, `reason` ∈ {scrubber,
	// cap}, `bytes_attempted`, `bytes_limit`, and `hits` (list of pattern
	// names for scrubber rejections). Severity is `warn` — the write was
	// refused at the boundary, no data was corrupted, but operators
	// should see it to tune allowlists.
	EntryMemoryWriteRejected EntryType = "memory.write_rejected"
	// EntryMemoryConsolidationProposed is the HITL-mode sibling of
	// EntryMemoryConsolidated. When the consolidator runs with
	// ProposalMode=true (env CREWSHIP_CONSOLIDATE_HITL=1) it writes the
	// extracted rules to {outputDir}/.proposed/proposal-{runID}.md and
	// inserts a memory_consolidation row into the inbox instead of
	// appending to learned-YYYY-MM-DD.md directly. The EntryMemoryConsolidated
	// final emit only fires after an operator approves the proposal via
	// POST /api/v1/consolidate/proposed/{id}/approve. Keeping the two
	// types distinct preserves the existing downstream semantic
	// ("rules are live now") on the old event.
	EntryMemoryConsolidationProposed EntryType = "memory.consolidation_proposed"
	// EntryMemoryWriteVerifierBlocked sibling of EntryMemoryWriteRejected
	// for the verifier rejection class. Distinct type so audit reviewers
	// can separate scrubber/cap denials (boundary policy) from verifier
	// denials (truthiness/citation policy) without parsing payloads.
	// Payload carries: tier, file, kind (stale_citation | contradiction),
	// detail (specific failure metadata). Severity is warn — the write
	// was refused at the boundary, no data was corrupted.
	EntryMemoryWriteVerifierBlocked EntryType = "memory.write_verifier_blocked"

	// EntryMemorySearched fires when an agent (or HTTP caller) issues a
	// memory search — FTS, hybrid, or whichever surface returned a
	// non-empty result. The payload captures `query` (the raw query
	// string), `scope` (own/crew_shared/...), `hit_count`, and
	// `hit_chunk_ids` (slice of chunk-/entry-ids that matched).
	// Downstream consumers:
	//   - the consolidator scoring path counts distinct hits per
	//     rule.Evidence to populate CandidateMetrics.RecallCount, and
	//     distinct query strings to populate UniqueQueries. Without
	//     these signals the Skill-promotion gate never fires in
	//     steady state (PRD §8.1 known follow-up — closed by this).
	//   - the observability dashboard renders search-frequency
	//     rollups per scope for capacity planning.
	// Severity is info — these are operational events, not warnings.
	EntryMemorySearched EntryType = "memory.searched"

	// EntryMemoryVersionsSwept fires when the per-workspace memory_versions
	// retention sweep deletes one or more rows from the audit-trail table.
	// Payload carries `workspace_id`, `deleted_count` (int), and
	// `retention_days` (the cutoff in days that was applied to this
	// workspace — extracted from workspaces.memory_config.versions_retention_days
	// or the 30-day default). Severity is `info`: routine maintenance.
	// Operators tail this entry to verify the sweep is firing and to
	// audit how much history has been trimmed. Blob GC is a separate
	// concern handled by the consolidate package's daily prune — this
	// event only describes row deletions.
	EntryMemoryVersionsSwept EntryType = "memory.versions_swept"

	// EntryMemoryConfigUpdated fires when an operator changes
	// workspaces.memory_config via the admin endpoint
	// PATCH /api/v1/admin/memory/config. Payload carries the
	// before-after diff (only fields that actually changed are
	// included; unchanged fields are omitted) so audit reviews can
	// trace exactly which retention policy was in effect for a
	// given window. Severity=Notice — the change itself isn't
	// alarming, but it IS load-bearing for compliance audits.
	EntryMemoryConfigUpdated EntryType = "memory.config_updated"

	// EntryMemorySkillProposed fires when the memory→Skills bridge stages
	// a learned rule as .proposed/skill-{slug}.md. Distinct from
	// EntryMemoryConsolidationProposed because the lifecycle is
	// independent: the same rule may produce both a learned-rule
	// proposal and a Skill proposal, and operators may approve one
	// without the other. Payload carries: skill_path, source_pattern,
	// composite, recall_count.
	EntryMemorySkillProposed EntryType = "memory.skill_proposed"

	// EntryMemorySkillApproved fires when an operator approves a staged
	// skill via POST /api/v1/skills/proposed/approve. The handler
	// imports the SKILL.md content through the canonical skills
	// importer and removes the staging file. Payload carries:
	// skill_id (the imported row's id), skill_path (the now-deleted
	// staging path), workspace_id, actor user id.
	EntryMemorySkillApproved EntryType = "memory.skill_approved"

	// EntryMemorySkillRejected fires when an operator rejects a staged
	// skill. The staging file is deleted; no DB row is created. Payload
	// carries: skill_path, workspace_id, actor user id, optional
	// rejection note.
	EntryMemorySkillRejected EntryType = "memory.skill_rejected"

	// Observability (Crow's Nest)
	EntryExecCommand       EntryType = "exec.command"
	EntryExecOutputChunk   EntryType = "exec.output_chunk"
	EntryNetworkPortOpen   EntryType = "network.port_opened"
	EntryNetworkPortClose  EntryType = "network.port_closed"
	EntryNetworkEgress     EntryType = "network.egress"
	EntryFileWritten       EntryType = "file.written"
	EntryContainerMetrics  EntryType = "container.metrics"
	EntryContainerSnapshot EntryType = "container.snapshot"

	// Presence
	EntryAgentStatus EntryType = "agent.status_change"

	// Checkpointing
	EntryCheckpointCreated  EntryType = "checkpoint.created"
	EntryCheckpointRestored EntryType = "checkpoint.restored"
	EntryForkCreated        EntryType = "fork.created"

	// EntryBackupChainResigned records that a restore re-signed this
	// workspace's journal hash chain and started it over at a new genesis
	// (#2226).
	//
	// It is emitted by exactly one operation: a FORKED restore
	// (`--as-workspace` / `--as-crew`), which regenerates every id the
	// chain's HMAC commits to and therefore has to recompute every
	// prev_hash/entry_hash under this installation's key. The chain that
	// results attests to THIS instance from this moment on — it carries no
	// cryptographic link back to the source workspace, whose signatures
	// covered ids that no longer exist.
	//
	// The entry is not a formality. Without it the fork's journal would
	// verify clean and read as unbroken provenance all the way back to the
	// source's genesis, which is a stronger claim than the data supports.
	// A fork deserves a new genesis; this is the row that says it got one,
	// and its payload names the bundle and the source workspace it was
	// forked from.
	EntryBackupChainResigned EntryType = "backup.chain_resigned"

	// Eval
	EntryEvalRunStarted EntryType = "eval.run_started"
	EntryEvalMetric     EntryType = "eval.metric"
	EntryEvalRegression EntryType = "eval.regression_detected"

	// Hooks
	EntryHookFired   EntryType = "hook.fired"
	EntryHookBlocked EntryType = "hook.blocked"
	// EntryHookDispatchError fires when Dispatch's lookup of the hooks
	// registered for an event fails before any hook could even be
	// evaluated — a DB error, not a policy decision. Kept distinct from
	// hook.fired/hook.blocked so operators (and the journal feed) can
	// tell "we couldn't check for hooks" apart from "a hook fired" or "a
	// hook blocked this". Gating callers fail closed, while observational
	// callers may continue. Severity is warn. Payload: event, error.
	EntryHookDispatchError EntryType = "hook.dispatch_error"

	// Automations — workspace-scoped rules that turn a journal event into a
	// deferred routine run (`automations` table, internal/automation). Both
	// entries below describe a REFUSAL, which is why they exist at all: an
	// automation that silently stops firing is indistinguishable from one
	// nobody triggered, and that is the failure mode a rules engine gets
	// support tickets for.
	//
	// EntryAutomationThrottled fires when an automation exceeds its
	// max_per_hour budget. Exactly ONE per automation per hour, never one
	// per dropped event — a storm that trips the cap 10,000 times must not
	// write 10,000 rows saying so. Payload: automation_id, automation_name,
	// event_type, max_per_hour, window_started_at.
	EntryAutomationThrottled EntryType = "automation.throttled"

	// Pipelines — declarative AI-authored workflows persisted per-
	// workspace and reusable across crews. See PIPELINES.md for the
	// full design. Run-level entries (started/completed/failed) frame
	// the run; step-level entries trace each individual step. Output
	// previews are truncated server-side to keep payload size bounded.
	// invoking_crew_id and author_crew_id are duplicated into the
	// payload so cross-crew reuse is queryable without a join.
	EntryPipelineRunStarted     EntryType = "pipeline.run.started"
	EntryPipelineRunCompleted   EntryType = "pipeline.run.completed"
	EntryPipelineRunFailed      EntryType = "pipeline.run.failed"
	EntryPipelineStepStarted    EntryType = "pipeline.step.started"
	EntryPipelineStepCompleted  EntryType = "pipeline.step.completed"
	EntryPipelineStepFailed     EntryType = "pipeline.step.failed"
	EntryPipelineStepValidation EntryType = "pipeline.step.validation_failed"
	EntryPipelineDryRun         EntryType = "pipeline.dry_run"

	// EntryPipelineStepSkipped / EntryPipelineStepRetrying make the two
	// non-terminal step outcomes first-class instead of overloading
	// completed/failed with a payload `kind` marker (the old scheme, which
	// left a skipped step indistinguishable from a completed one at the
	// storage layer and a retry attempt indistinguishable from a terminal
	// failure). entry_type is an unconstrained TEXT column (no CHECK), so
	// these need no schema migration; pre-existing rows keep their
	// completed+kind=skipped / failed+kind=retry shape and readers fall
	// back to the marker for those. Skipped = the step's If condition was
	// false so it never ran; Retrying = a transient failure the retry
	// policy is about to swallow (the terminal completed/failed still
	// follows).
	EntryPipelineStepSkipped  EntryType = "pipeline.step.skipped"
	EntryPipelineStepRetrying EntryType = "pipeline.step.retrying"

	// EntryPipelineStepContainerReady records how long a step spent acquiring
	// its crew container (the EnsureCrewRuntime call), isolating the
	// container-provision cost from the LLM/tool time buried in the step's
	// total duration. It is what the #902 prewarm shortens: a prewarmed run's
	// first step finds the container warm (small duration_ms), a cold run pays
	// the provision inline (large duration_ms). Exposed via `routine logs` so
	// claim→first-step is measurable without guessing (#911). trace_id ==
	// run.id, payload carries step_id + duration_ms.
	EntryPipelineStepContainerReady EntryType = "pipeline.step.container_ready"

	// EntryPipelineScheduleCircuitBreaker fires when a schedule auto-disables
	// after K consecutive FAILED fires (#1405). Payload carries schedule_id,
	// consecutive_failures, max_consecutive_failures. Distinct from
	// EntryPipelineRunFailed (which is per-run) — this is the schedule-level
	// breaker tripping, emitted exactly once per trip.
	EntryPipelineScheduleCircuitBreaker EntryType = "pipeline.schedule.circuit_breaker_tripped"

	// EntryPipelineScheduleMissedOccurrences fires once per fire when a
	// schedule's next_run_at lagged far enough behind "now" that more
	// than one cron occurrence would have fired had the process been
	// continuously up (#1409) — e.g. a server restart / downtime window.
	// fireOne still only fires ONCE for the current occurrence (this is
	// observability, not a backfill); the entry makes the otherwise-silent
	// gap visible. Payload carries schedule_id, missed_count, window_start,
	// window_end (RFC3339).
	EntryPipelineScheduleMissedOccurrences EntryType = "pipeline.schedule.missed_occurrences"

	// EntryPipelineRunsSwept fires when the per-workspace pipeline_runs
	// retention sweep (internal/pipeline/retention.go) deletes one or more
	// terminal runs older than the configured window. run_tags cascade-
	// deletes with their run (ON DELETE CASCADE); warnings_json is a
	// column on the row itself, not a separate table — nothing else needs
	// cleanup. Payload carries `workspace_id`, `deleted_count` (int),
	// `retention_days`, and `keep_last_n_per_pipeline`. Severity is info:
	// routine maintenance, mirrors EntryMemoryVersionsSwept's contract.
	// See issue #1407.
	EntryPipelineRunsSwept EntryType = "pipeline.runs_swept"

	// EntryAutomationDepthExceeded fires when a COMPOSED edge is refused
	// because the chain it belongs to is already at the depth cap
	// (pipeline.MaxChainDepth). Composition — an automation firing a routine
	// that acts on an issue whose change fires an automation — makes cycles
	// trivially constructible, and a cap that refuses silently is a cap
	// nobody can debug: this entry is how an operator finds out that a loop
	// exists at all. Payload carries chain_depth (the depth that would have
	// been created), max_chain_depth, chain_origin, and the edge that was
	// refused (pipeline_slug / run_id for a call_pipeline edge). Severity is
	// error — a refused edge means work the author expected did not happen.
	//
	// Namespaced `automation.` rather than `pipeline.` because the cap is a
	// property of the composition substrate, not of routines: the automation
	// dispatcher refuses at the same ceiling, through the same
	// pipeline.GuardChainDepth, and emits the same type.
	EntryAutomationDepthExceeded EntryType = "automation.depth_exceeded"

	// EntryRunSessionInit is the provenance of the agent CLI session a run
	// happens inside, taken from the CLI's own session-init event and written
	// once per run: which binary answered (cli_version), which model it
	// resolved to, which credential path took (api_key_source, constrained to
	// a known set), the permission mode, the cwd, and COUNTS of the tool /
	// skill / capability inventory it started with. trace_id == run.id.
	//
	// It exists for one field in particular. mcp_server_errors lists
	// --mcp-config entries the CLI SKIPPED at startup after failing
	// validation; the run then continues and exits 0, so an agent that lost
	// crewship-memory that way looks perfectly healthy while being quietly
	// less capable. Severity is info normally and error when that list is
	// non-empty — the same call EntrySidecarStale makes, for the same reason.
	//
	// The payload carries each skipped server's `name` and closed-category
	// `type` (unknown_type / url_missing_type / invalid_config / …) but NEVER
	// its free-text `message`: the emit site sits upstream of the credential
	// scrubber and journal rows are hash-chained, so anything copied in is
	// both unscrubbed and unredactable. The verbatim line stays available in
	// the run's exec.output_chunk entry.
	EntryRunSessionInit EntryType = "run.session_init"

	// EntryRunAgentSpan is one INTERNAL action of an agent_run step — a single
	// tool the agent invoked (Bash/Write/Edit/Read/MCP/HTTP). It is the leaf of
	// the drillable run-trace tree (run → step → tool). trace_id == run.id (so
	// the runs API can pull every sub-span of a run via the trace_id index),
	// actor_id == run.id, and the payload carries step_id, seq, kind, name,
	// detail, started_at, duration_ms, status, attributes. Volume is bounded at
	// the capture site (cap per step + detail truncation) so a chatty agent
	// can't flood the journal. Severity is info (warn when the tool errored).
	EntryRunAgentSpan EntryType = "run.agent_span"

	// System
	EntrySystemCompaction             EntryType = "system.compaction"
	EntrySystemMigration              EntryType = "system.migration"
	EntrySystemHookToggled            EntryType = "system.hook_toggled"
	EntrySystemConsolidationTriggered EntryType = "system.consolidation_triggered"
	EntrySystemConsolidationCompleted EntryType = "system.consolidation_completed"
	// EntrySidecarStale (#1160): a crew container is serving an OLD
	// bind-mounted crewship-sidecar from before the last redeploy (#1008
	// detection), so memory recall and egress policy may be silently
	// degraded. Emitted at severity:error by the orchestrator so the
	// condition lands in the activity feed instead of only stdout — the
	// channel class nobody watched when #1008 first happened. Remediation:
	// `crewship crew restart-agents`.
	EntrySidecarStale EntryType = "sidecar.stale"

	// EntryImageStale (#1845): a crew's RUNNING container was created from a
	// manifest that its image tag no longer resolves to. Deliberately a
	// different type from EntrySidecarStale rather than a second flavour of it,
	// because the two say different things and want different reactions:
	//
	//   sidecar.stale — the code executing INSIDE the container right now is
	//     not the code this server shipped. Memory recall and egress policy are
	//     degraded as you read it. Severity error; fix is rebuild + recopy the
	//     sidecar binary, then recycle.
	//   image.stale   — the container is a faithful snapshot of an older
	//     release. Nothing is misbehaving; it is simply not current. Severity
	//     warn; fix is `crewship crew refresh-image`.
	//
	// Emitted once per (crew, running digest, resolved digest) by the daily
	// image-freshness sweep, never per run: the condition persists until an
	// operator acts, and a per-run emitter would write the same row hundreds of
	// times a day. See internal/server/image_freshness.go.
	EntryImageStale EntryType = "image.stale"

	// Credentials
	// EntryCredentialAutoAssignFailed: a single autoAssignCredentials step failed
	// (list/scan/insert). Operators see this when a template/Captain/internal
	// flow tried to wire workspace AI credentials and one row didn't make it.
	EntryCredentialAutoAssignFailed EntryType = "credential.auto_assign_failed"
	// EntryCredentialAutoAssignEmpty: autoAssignCredentials ran but found zero
	// Anthropic credentials in the workspace, so the agent will need a manual
	// assignment before it can chat. Most common cause of "silent run" reports.
	EntryCredentialAutoAssignEmpty EntryType = "credential.auto_assign_empty"
	// EntryCredentialLeaseIssued (#1373): an agent's credential grant was
	// (re-)issued as a short-lived LEASE rather than left standing — minted on a
	// Keeper ALLOW or on the approval of an agent-proposed CREDENTIAL escalation
	// when the workspace has keeper_governance_settings.auto_lease_seconds set.
	// Payload carries the source, the resulting expiry and the authorising
	// request id. Not in the compactor's allowlist, so it is never rolled up:
	// this is the record that explains why a credential stopped working.
	EntryCredentialLeaseIssued EntryType = "credential.lease_issued"

	// Skills — registry-level + per-agent assignment lifecycle. Skill rows
	// are global (no workspace_id column), but every event carries the
	// originating workspace so the journal stays workspace-scoped on read.
	// `allow_unsafe_license` is captured as a metadata flag on the imported
	// entry so a compliance audit can list every license-gate override
	// without correlating across tables.
	EntrySkillImported   EntryType = "skill.imported"
	EntrySkillDeleted    EntryType = "skill.deleted"
	EntrySkillAssigned   EntryType = "skill.assigned"
	EntrySkillUnassigned EntryType = "skill.unassigned"
	// EntrySkillInvoked fires when an agent actually calls one of its
	// assigned skills (matched on the orchestrator tool-call hot path).
	// Payload carries `skill_id`, `skill_slug`, `agent_id`, `tool_name`,
	// `exit_code`, and `usage_count` (the post-increment denormalised
	// counter on the skills row). This is the telemetry source the F4.1 skill-review
	// sweep reads to decide "is this skill actually in use?" — every
	// invocation also lands a skill_invocations audit row + bumps the
	// skills lifecycle counters in the same transaction.
	EntrySkillInvoked EntryType = "skill.invoked"

	// Audit — workspace CRUD lifecycle. These mirror writes to the
	// audit_logs table, dual-emitted from WriteAuditLog so a compliance
	// reviewer can read the same events from either surface (legacy
	// audit_logs query path or the unified journal). Payload carries
	// `entity_type`, `entity_id`, `action`, plus any metadata the call
	// site supplied. Severity is `notice` so the events surface in the
	// Timeline without being lost in `info` chatter.
	EntryAuditEntityCreated  EntryType = "audit.entity_created"
	EntryAuditEntityUpdated  EntryType = "audit.entity_updated"
	EntryAuditEntityDeleted  EntryType = "audit.entity_deleted"
	EntryAuditEntityRestored EntryType = "audit.entity_restored"

	// Provisioning — container lifecycle for crew runtime. Emit at
	// state transitions in the Provisioner so operators see "the build
	// queue moved" without tailing slog. `queued` fires before the
	// docker build kicks off; `building` flips when image build starts;
	// `complete` lands when the runtime is ready to accept assignments;
	// `failed` carries the original error in payload.error.
	EntryProvisioningQueued   EntryType = "provisioning.queued"
	EntryProvisioningBuilding EntryType = "provisioning.building"
	EntryProvisioningComplete EntryType = "provisioning.complete"
	EntryProvisioningFailed   EntryType = "provisioning.failed"

	// EntryProvisioningBuildFailed is the durable record of a devcontainer
	// feature-BUILD failure, carrying the bounded, scrubbed BuildKit stderr
	// tail in Payload["detail"] (plus Payload["error"] and ["tag"]). It is the
	// post-hoc diagnostic surface (#829): the live WS event is ephemeral, so
	// this journal row is what `crewship crew provision status` reads back after
	// the in-memory job's TTL to show WHY a build failed.
	EntryProvisioningBuildFailed EntryType = "provisioning.build_failed"

	// EntryProvisioningStep is one fine-grained, structured step in the
	// container-preparation pipeline (resolve_features → image_build →
	// per-feature install → container_create → containerEnv_apply → ready, plus
	// cache_hit and per-step failures). Distinct from the coarse
	// queued/building/complete markers above: those bracket the whole job, this
	// records every step so an operator can see exactly where a build got stuck
	// across thousands of runs. Payload carries the full ProvisionEvent fields
	// (step, feature, status, detail, error, tag, duration_ms). Severity is
	// warn for failures, info otherwise.
	EntryProvisioningStep EntryType = "provisioning.step"

	// Chat — user↔agent conversation turns. Captures the trigger that
	// kicks off a series of agent actions, so the Timeline can answer
	// "what did the user ask?" alongside "what did the agent do?".
	// Payload contains the message text capped to PreviewLen chars in
	// summary; full content in payload.content. chat_id + agent_id +
	// (optional) crew_id wire it back to the conversation surface.
	EntryChatUserMessage   EntryType = "chat.user_message"
	EntryChatAgentResponse EntryType = "chat.agent_response"

	// Agent — runtime errors caught at the orchestrator boundary.
	// Panic recoveries, unexpected shutdowns, provider stream errors
	// land here so they are visible in the same surface as exec.command
	// outputs they were processing when things went wrong.
	EntryAgentError EntryType = "agent.error"

	// Issues — mission lifecycle beyond the status changes
	// EntryMissionStatus already covers. Creation and assignment were
	// previously invisible to the journal: an issue appearing or changing
	// hands left no trace outside the mission_activity table and a
	// WebSocket event, so neither Activity nor an external notification
	// could see it happen.
	EntryMissionCreated  EntryType = "mission.created"
	EntryMissionAssigned EntryType = "mission.assigned"

	// Notifications — the outbound delivery attempt itself. An external
	// send is a side effect that leaves the instance, and until now it was
	// visible only in the notification_deliveries table behind an
	// admin-only endpoint. Emitting it here puts "this went to Slack" on
	// the same Activity timeline as the event that caused it, with its own
	// icon.
	//
	// These MUST NOT map to a notification category — see
	// notifyroute.CategoryForJournalType, which rejects them explicitly.
	// Routing a delivery record back into the router would notify about
	// notifying, forever.
	EntryNotificationDelivered EntryType = "notification.delivered"
	EntryNotificationFailed    EntryType = "notification.failed"
	EntryNotificationDropped   EntryType = "notification.dropped"

	// Pages — docs/prd/pages.md §5 and §7.1b. Unknown journal types are
	// forwarded by design (feed_filter.go:33-35), so these reach the
	// activity feed with no filter change.
	//
	// EntryPageProduceDenied records a push to a panel the caller does
	// not hold; it was previously declared locally in
	// internal/api/pages_data.go and should move to use this constant
	// instead of redeclaring the string.
	EntryPageProduceDenied EntryType = "page.produce_denied"

	// EntryPagePanelUpdated is emitted on every successful panel push
	// (§5) and doubles as the realtime broadcast type consumed by
	// hooks/use-realtime.tsx's VALID_REALTIME_TYPES allowlist.
	EntryPagePanelUpdated EntryType = "page.panel.updated"

	// EntryPageGrantAdded and EntryPageGrantRemoved record every change
	// to a page's ACL (§7.1b) — actor and subject are carried in the
	// payload. "An ACL nobody can audit is not a security control."
	EntryPageGrantAdded   EntryType = "page.grant_added"
	EntryPageGrantRemoved EntryType = "page.grant_removed"

	// EntryPageActionDispatched records a click on a panel's declared action
	// (§8b.2): who clicked, which action, and which routine the SERVER resolved
	// it to. The routine is in the payload rather than assumed from the action
	// id because the point of the dispatch design is that the server chose it —
	// so the audit trail is where that choice is written down.
	EntryPageActionDispatched EntryType = "page.action.dispatched"

	// EntryPagePanelStale is written ONCE per lapse by the freshness
	// sweeper: a panel that was reporting is now stale or failed (§4).
	// It is the producer behind notify.CategoryPagesStale, which was a
	// registered category with nothing emitting it. Payload: page,
	// page_id, panel, verdict, age_seconds, sla_seconds, reason, and —
	// when the panel declared on_failure — issue_id / issue_identifier.
	//
	// Once per lapse rather than once per tick: the edge is recorded in
	// page_panel_alerts, so a panel quiet for a week produces one entry
	// and one issue rather than one of each per minute.
	EntryPagePanelStale EntryType = "page.panel.stale"

	// EntryPagePanelRecovered is the other edge: data arrived inside the
	// SLA again and the open alert was cleared. Emitted so "it fixed
	// itself at 03:12" is answerable from the journal rather than from
	// the absence of anything.
	EntryPagePanelRecovered EntryType = "page.panel.recovered"

	// EntryPageWakeFired records a wake gate waking a crew (§5): the
	// threshold held for its window, the automation matched, and an issue
	// was opened. Payload: page, page_id, panel, gate, crew, writes,
	// issue_id, issue_identifier, coalesced_events.
	EntryPageWakeFired EntryType = "page.wake.fired"

	// EntryPageSpecChanged records that a page's ARRANGEMENT changed — a
	// panel added, removed, reordered or edited. It is the event
	// `refresh: on:panels-changed` is armed on (§12 v1.1), which is why it
	// is a journal entry rather than only the websocket broadcast that
	// already existed: an automation matcher can only see the journal.
	//
	// Emitted on create and on any update whose panel list differs, and
	// NOT on a rename — the page's metadata is not its arrangement. That
	// "differs" is a fingerprint over the panel list, so a producer that
	// re-applies the same manifest emits nothing and cannot refresh
	// itself in a circle. Payload: page, page_id, panels, created,
	// fingerprint.
	EntryPageSpecChanged EntryType = "page.spec.changed"
)

// Severity is a coarse importance level used by filters and retention. UI
// surfaces warn/error prominently; compaction keeps those indefinitely
// while rolling up info/notice into daily summaries.
type Severity string

const (
	SeverityInfo   Severity = "info"
	SeverityNotice Severity = "notice"
	SeverityWarn   Severity = "warn"
	SeverityError  Severity = "error"
)

// Priority is a user-facing importance marker orthogonal to Severity.
// Severity answers "how alarming is this?" — Priority answers "how
// long should we remember it and how prominently should it surface
// at recall time?". Three explicit markers (`permanent` / `high` /
// `pin`) plus the default let operators and lead agents annotate
// entries so the consolidator and compactor can make smarter
// keep/drop decisions.
//
// The enum is deliberately small. 'normal' is the implicit default for
// every emit (DB column defaults to 'normal' too) so the vast majority
// of entries flow through with no extra annotation.
type Priority string

const (
	// PriorityNormal is every entry's default; no special treatment
	// at recall or compaction.
	PriorityNormal Priority = "normal"

	// PriorityHigh boosts importance score at episodic recall, but
	// is still subject to normal compaction rules — use this for
	// "this matters for the next few weeks, not forever".
	PriorityHigh Priority = "high"

	// PriorityPin snapshots the entry into /crew/shared/.memory/pins.md
	// at the next consolidation run so operators can see it
	// alongside curated memory without a journal query. Pin is for
	// "I want future agents to see this every session", e.g. a crew
	// convention or a mission-critical caveat.
	PriorityPin Priority = "pin"

	// PriorityPermanent guarantees the entry is never compacted AND
	// is extracted to learned-*.md without waiting for the normal
	// 10-entry threshold or 6h cadence. Use sparingly — every
	// permanent entry survives the life of the database.
	PriorityPermanent Priority = "permanent"
)

// ValidPriority returns true when p is one of the four allowed values.
// Callers that build entries from untrusted input (HTTP handlers, CLI
// flags) should validate before emitting so a bad string doesn't wedge
// the DB CHECK constraint.
func ValidPriority(p Priority) bool {
	switch p {
	case PriorityNormal, PriorityHigh, PriorityPin, PriorityPermanent:
		return true
	}
	return false
}

// ActorType identifies who/what produced an entry. Used for filtering
// ("show me only what agents did") and for policy decisions (shell hooks
// can only be registered by users, not by agents).
type ActorType string

const (
	ActorAgent        ActorType = "agent"
	ActorUser         ActorType = "user"
	ActorSystem       ActorType = "system"
	ActorKeeper       ActorType = "keeper"
	ActorSidecar      ActorType = "sidecar"
	ActorOrchestrator ActorType = "orchestrator"
)

// Scope identifies which workspace/crew/agent/mission an entry belongs to.
// WorkspaceID is always required; the rest narrow the scope. Any nil
// pointer means "not scoped to that dimension" (e.g. a workspace-level
// system event has no crew or agent).
type Scope struct {
	WorkspaceID string
	CrewID      string // optional
	AgentID     string // optional
	MissionID   string // optional
}

// Entry is one record in the journal. Callers build it via Emit helpers
// (below) or by constructing the struct directly. Once written, entries
// are immutable; corrections are new entries with Refs.ParentEntryID set.
//
// ID and TS are populated by Emit if the caller leaves them zero — most
// call sites should leave them zero. TraceID/SpanID are populated from
// context.Context by the telemetry middleware; callers can override.
type Entry struct {
	ID          string
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	TS          time.Time
	Type        EntryType
	Severity    Severity
	Priority    Priority // zero-value → DB default 'normal'; see Priority doc
	ActorType   ActorType
	ActorID     string
	Summary     string
	Payload     map[string]any
	Refs        map[string]any
	TraceID     string
	SpanID      string
	ExpiresAt   *time.Time

	// flushBarrierAck is an internal sentinel used by Writer.Flush.
	// When non-nil, the worker treats this Entry as a barrier rather
	// than a row to persist — the barrier rides the same queue as
	// real entries, so the worker can only close the ack after it
	// has drained every Entry that was queued before Flush was
	// called. Field is unexported so external packages cannot set it
	// — Go's visibility rules are the actual enforcement mechanism.
	flushBarrierAck chan struct{}
}

// Validate checks that the entry has the minimum fields the schema requires.
// Called by Emit before the write is queued so the error is returned to the
// caller synchronously rather than logged deep in the writer goroutine.
//
// Side effect: defaults empty Severity to SeverityInfo on the receiver.
// This keeps every Emit call site from having to set a field that is
// almost always "info" — the DB column already defaults to "info" too,
// so setting it here just makes the in-memory Entry match what would
// land on disk. Not pure validation, but documented so the behavior
// isn't surprising when this method is reused elsewhere.
func (e *Entry) Validate() error {
	if e.WorkspaceID == "" {
		return errors.New("journal: workspace_id required")
	}
	if e.Type == "" {
		return errors.New("journal: entry_type required")
	}
	if e.ActorType == "" {
		return errors.New("journal: actor_type required")
	}
	if e.Summary == "" {
		return errors.New("journal: summary required")
	}
	if e.Severity == "" {
		e.Severity = SeverityInfo
	}
	if e.Priority == "" {
		e.Priority = PriorityNormal
	}
	if !ValidPriority(e.Priority) {
		return fmt.Errorf("journal: invalid priority %q (allowed: normal|high|pin|permanent)", e.Priority)
	}
	return nil
}

// payloadJSON encodes Payload to a JSON string the SQL driver can bind.
// Nil / empty payloads serialize to "{}" so the column's NOT NULL / default
// stays satisfied without a driver-side nil check.
func (e *Entry) payloadJSON() (string, error) {
	if len(e.Payload) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(e.Payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (e *Entry) refsJSON() (string, error) {
	if len(e.Refs) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(e.Refs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
