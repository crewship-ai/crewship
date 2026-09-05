// Package harbormaster is the human-in-the-loop (HITL) approval workflow
// engine. Agents call Gate before high-risk actions; if a configured rule
// matches, the action is queued in approvals_queue and either logged-and-
// continued (Async) or paused until a human decides (Sync). Decisions and
// timeouts emit journal entries so the audit trail is complete.
//
// The package intentionally has a tiny surface: a Store for queue access,
// an Evaluator for rule matching, and a Gate function that ties them
// together. UI/API live elsewhere; the package only depends on the journal
// Emitter and a *sql.DB.
package harbormaster

import (
	"regexp"
	"time"
)

// Kind classifies what triggered the approval. The string is persisted in
// approvals_queue.kind so renames require a migration.
type Kind string

const (
	KindToolCall          Kind = "tool_call"
	KindCostThreshold     Kind = "cost_threshold"
	KindDestructiveOp     Kind = "destructive_op"
	KindTargetEnvironment Kind = "target_environment"
	KindCustom            Kind = "custom"
	// KindEphemeralHire is a guided-autonomy ephemeral hire waiting for an
	// operator decision (issue #1209). Unlike the other kinds it is NOT
	// enqueued by Gate() — the Hire endpoint writes it directly so hire
	// waitpoints show up in the standard approvals surface instead of
	// being decidable only through `hire approve`.
	KindEphemeralHire Kind = "ephemeral_hire"
	// KindAutonomyGate is a creation an agent made through the sidecar's
	// internal API that the crew's autonomy_level held for operator review
	// (#1768) — a crew, a persistent agent, a mission, a cron schedule. Like
	// KindEphemeralHire it is NOT enqueued by Gate(): the backend adapter
	// writes it directly, alongside a blocking inbox item, so the hold shows
	// up on the standard approvals surface. Deciding it releases the
	// sentinel that keeps the created row inert (see
	// internal/api/internal_autonomy_gate.go).
	//
	// approvals_queue.kind carries no CHECK constraint, so this value needs
	// no migration; renaming it would strand rows written by an older build.
	KindAutonomyGate Kind = "autonomy_gate"
)

// Status mirrors the CHECK constraint on approvals_queue.status. Callers
// should use the constants rather than string literals so a typo is a
// compile error.
type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusTimeout   Status = "timeout"
	StatusCancelled Status = "cancelled"
)

// Mode controls how Gate behaves once a request is enqueued. The naming
// matches the LangGraph interrupt() concept: None bypasses approval, Async
// records the request and lets the agent keep going, Sync blocks the
// caller until a human decides (or the request times out).
type Mode int

const (
	ModeNone Mode = iota
	ModeAsync
	ModeSync
)

// String renders Mode for logs.
func (m Mode) String() string {
	switch m {
	case ModeAsync:
		return "async"
	case ModeSync:
		return "sync"
	default:
		return "none"
	}
}

// Request is the in-memory shape of an approvals_queue row. Callers fill
// it in before calling Store.Enqueue; the store assigns ID/CreatedAt/
// TimeoutAt and writes the row.
//
// The JSON tags are load-bearing, not decoration: ApprovalsHandler.List
// and .Get serialize this struct straight onto the wire. Without tags the
// API answered "ID"/"Kind"/"CreatedAt" while lib/types/approvals.ts and
// cmd/gen-openapi both declared snake_case, so every browser client
// rejected the body in zod and rendered an empty approvals queue. The Go
// CLI was the only consumer that worked, because encoding/json matches
// field names case-insensitively on the way IN — which is also why no Go
// test caught it. TestApprovals_WireShape_IsSnakeCase asserts the emitted
// keys, and is the guard that must fail if these tags are ever dropped.
type Request struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	CrewID          string         `json:"crew_id"`
	AgentID         string         `json:"agent_id"`
	MissionID       string         `json:"mission_id"`
	RequestedBy     string         `json:"requested_by"`
	Kind            Kind           `json:"kind"`
	Reason          string         `json:"reason"`
	Payload         map[string]any `json:"payload"`
	Status          Status         `json:"status"`
	DecidedBy       string         `json:"decided_by"`
	DecidedAt       *time.Time     `json:"decided_at"`
	DecisionComment string         `json:"decision_comment"`
	TimeoutAt       *time.Time     `json:"timeout_at"`
	CreatedAt       time.Time      `json:"created_at"`
	// TimeoutSecs is consulted by Enqueue when TimeoutAt is zero. Default
	// 3600 (one hour). Stored only on the in-memory struct, not persisted
	// — and therefore never serialized: a client that read it would be
	// reading a value the database does not have.
	TimeoutSecs int `json:"-"`
	// RoutineVersion is §9.8's one addition to the decision-receipt columns
	// (PRD-ISSUES-AND-ROUTINES-2026, work package B10, #2364): "was it the
	// SAME version that then ran?" Zero means this approval has nothing to
	// do with a routine's authored definition — most approvals_queue rows
	// (a credential use, a hire) — and is persisted as SQL NULL, not 0, so
	// it never reads as "version zero of a real routine". Set only by a
	// caller that resolved a concrete pipeline_versions.version, e.g.
	// internal/api/internal_routines.go's autonomy-gated schedule creation.
	RoutineVersion int `json:"routine_version,omitempty"`
}

// Decision is what Gate returns to the caller. Pending=true means the
// request was enqueued in async mode and the caller should continue;
// Approved/Denied/TimedOut reflect a sync resolution. RequestID is always
// set so async callers can correlate later.
type Decision struct {
	Pending   bool
	Approved  bool
	Denied    bool
	TimedOut  bool
	RequestID string
	Status    Status
	DecidedBy string
	Comment   string
	Reason    string
	Kind      Kind
	NotGated  bool // true when no rule matched and Gate fell through
}

// RuleMatcher describes one matching rule. A rule fires when ANY of its
// non-zero conditions match. Composing several specific rules is preferred
// over building one super-rule with broad disjunctions.
type RuleMatcher struct {
	// Name is for logs and for the journal Reason; empty falls back to Kind.
	Name string
	// ToolPattern is a compiled regex matched against the tool name. nil
	// means "don't match on tool".
	ToolPattern *regexp.Regexp
	// CostThresholdUSD fires if args["cost_estimate_usd"] is a number >=
	// this value. Zero disables the check.
	CostThresholdUSD float64
	// TargetEnvPatterns are matched against args["target"] / args["host"]
	// / args["environment"] (case-insensitive substring). Empty disables.
	TargetEnvPatterns []string
	// Kinds restricts the match to specific KindXxx values when the caller
	// passed a kind hint. Empty matches all.
	Kinds []Kind
	// RequireWhen, if non-nil, is a free-form predicate evaluated last —
	// returning true forces approval even when none of the structural
	// conditions matched.
	RequireWhen func(tool string, args map[string]any) bool
	// MapsToKind is the Kind written to approvals_queue when this rule
	// fires. Defaults are filled by the constructors below.
	MapsToKind Kind
}
