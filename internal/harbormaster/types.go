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
	// KindAgentHire marks synthetic read-model rows only: staged
	// (PENDING_REVIEW) ephemeral hires surfaced through the approvals
	// listing (#1209). These rows are projected live from the agents
	// table and are NEVER persisted to approvals_queue — the hire
	// decision state machine stays in the approve-hire handler.
	KindAgentHire Kind = "agent_hire"
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
// The json tags are the wire contract for GET /api/v1/approvals — both
// first-party consumers (the CLI's snake_case struct tags and the
// dashboard's zod approvalRowSchema) expect snake_case keys. The struct
// previously marshalled with Go-cased field names, which neither client
// could fully decode (crew_id/created_at/decided_by arrived empty).
type Request struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	CrewID          string         `json:"crew_id"`
	AgentID         string         `json:"agent_id"`
	MissionID       string         `json:"mission_id"`
	RequestedBy     string         `json:"requested_by"`
	Kind            Kind           `json:"kind"`
	Reason          string         `json:"reason"`
	Payload         map[string]any `json:"payload,omitempty"`
	Status          Status         `json:"status"`
	DecidedBy       string         `json:"decided_by"`
	DecidedAt       *time.Time     `json:"decided_at"`
	DecisionComment string         `json:"comment"`
	TimeoutAt       *time.Time     `json:"timeout_at"`
	CreatedAt       time.Time      `json:"created_at"`
	// TimeoutSecs is consulted by Enqueue when TimeoutAt is zero. Default
	// 3600 (one hour). Stored only on the in-memory struct, not persisted.
	TimeoutSecs int `json:"-"`
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
