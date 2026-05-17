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
type EntryType string

const (
	// Communication
	EntryPeerConversation EntryType = "peer.conversation"
	EntryPeerEscalation   EntryType = "peer.escalation"
	EntryMessageBroadcast EntryType = "message.broadcast"
	EntryAgentMentioned   EntryType = "agent.mentioned"

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

	// Eval
	EntryEvalRunStarted EntryType = "eval.run_started"
	EntryEvalMetric     EntryType = "eval.metric"
	EntryEvalRegression EntryType = "eval.regression_detected"

	// Hooks
	EntryHookFired   EntryType = "hook.fired"
	EntryHookBlocked EntryType = "hook.blocked"

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

	// System
	EntrySystemCompaction             EntryType = "system.compaction"
	EntrySystemMigration              EntryType = "system.migration"
	EntrySystemHookToggled            EntryType = "system.hook_toggled"
	EntrySystemConsolidationTriggered EntryType = "system.consolidation_triggered"
	EntrySystemConsolidationCompleted EntryType = "system.consolidation_completed"

	// Credentials
	// EntryCredentialAutoAssignFailed: a single autoAssignCredentials step failed
	// (list/scan/insert). Operators see this when a template/Captain/internal
	// flow tried to wire workspace AI credentials and one row didn't make it.
	EntryCredentialAutoAssignFailed EntryType = "credential.auto_assign_failed"
	// EntryCredentialAutoAssignEmpty: autoAssignCredentials ran but found zero
	// Anthropic credentials in the workspace, so the agent will need a manual
	// assignment before it can chat. Most common cause of "silent run" reports.
	EntryCredentialAutoAssignEmpty EntryType = "credential.auto_assign_empty"

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
// Severity answers "how alarming is this?" — Priority answers "how long
// should we remember it and how prominently should it surface at recall
// time?". Inspired by OpenClaw Auto-Dream's ⚠️ PERMANENT / 🔥 HIGH /
// 📌 PIN markers: operators and lead agents annotate entries so the
// consolidator and compactor can make smarter keep/drop decisions.
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
