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

	// System
	EntrySystemCompaction             EntryType = "system.compaction"
	EntrySystemMigration              EntryType = "system.migration"
	EntrySystemHookToggled            EntryType = "system.hook_toggled"
	EntrySystemConsolidationTriggered EntryType = "system.consolidation_triggered"
	EntrySystemConsolidationCompleted EntryType = "system.consolidation_completed"
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
