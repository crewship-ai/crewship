// Package hooks implements the lifecycle intercept layer for Crewship.
//
// Hooks let workspace admins run arbitrary logic when platform events fire —
// pre/post task delegation, agent start/stop, tool calls, LLM calls, memory
// writes, peer conversations, approvals, budget overruns, guardrail trips.
// They are the orchestration-layer equivalent of Claude Code's shell hooks,
// but generalized across three handler kinds: shell commands, HTTP webhooks,
// and LLM subagents.
//
// The package is split into:
//
//   - types.go       — Event / HandlerKind / Hook / Matcher / Result
//   - store.go       — Register / Get / List / Enable / Disable / Delete
//   - matcher.go     — EventContext matching against Hook.Matcher
//   - shell.go       — exec.CommandContext sandboxed handler
//   - http.go        — HTTP POST handler with optional HMAC signing
//   - subagent.go    — interface that the orchestrator wires up later
//   - dispatcher.go  — Dispatch() entry point used by call sites
//
// Call sites (orchestrator, bridge, paymaster, keeper, ...) import this
// package and call Dispatch() at the relevant lifecycle points. The package
// has no dependency on the orchestrator or HTTP router — it only speaks SQL
// and journal.Emitter.
package hooks

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event names every lifecycle point a hook can be wired up to. The string
// form is what lands in the hooks_config.event column, so values are stable
// — renaming one breaks every existing registration. New events are fine to
// add as long as the corresponding Dispatch call site is also updated.
type Event string

const (
	// Task delegation — a LEAD agent handing work to an AGENT.
	EventPreTaskDelegation  Event = "pre_task_delegation"
	EventPostTaskDelegation Event = "post_task_delegation"

	// Agent container lifecycle. PreAgentStart fires before the container
	// boots; PostAgentStop fires after it has been torn down.
	EventPreAgentStart Event = "pre_agent_start"
	EventPostAgentStop Event = "post_agent_stop"

	// Tool invocation inside an agent — e.g. Bash, Read, Edit, MCP tools.
	EventPreToolCall  Event = "pre_tool_call"
	EventPostToolCall Event = "post_tool_call"

	// LLM calls. Fires around every provider round-trip, regardless of
	// whether the agent is acting on its own or via Keeper approval.
	EventPreLLMCall  Event = "pre_llm_call"
	EventPostLLMCall Event = "post_llm_call"

	// Memory writes — consolidation, summary, manual writes from agents.
	EventPreMemoryWrite  Event = "pre_memory_write"
	EventPostMemoryWrite Event = "post_memory_write"

	// Peer-to-peer agent conversations (via the peer bridge).
	EventPrePeerConversation  Event = "pre_peer_conversation"
	EventPostPeerConversation Event = "post_peer_conversation"

	// Policy / limit events. Fire once per triggering condition so a hook
	// can route them to pagerduty, slack, the journal, a Captain, etc.
	EventOnApprovalRequested  Event = "on_approval_requested"
	EventOnBudgetExceeded     Event = "on_budget_exceeded"
	EventOnGuardrailTriggered Event = "on_guardrail_triggered"
)

// AllEvents is the stable iteration order used by the registration UI and
// by audit exports. Tests also use it to verify Event constants stay in
// sync with the dispatcher's switch statements.
var AllEvents = []Event{
	EventPreTaskDelegation,
	EventPostTaskDelegation,
	EventPreAgentStart,
	EventPostAgentStop,
	EventPreToolCall,
	EventPostToolCall,
	EventPreLLMCall,
	EventPostLLMCall,
	EventPreMemoryWrite,
	EventPostMemoryWrite,
	EventPrePeerConversation,
	EventPostPeerConversation,
	EventOnApprovalRequested,
	EventOnBudgetExceeded,
	EventOnGuardrailTriggered,
}

// ValidateEvent reports whether e is one of the events declared above.
//
// Nothing in the schema constrains hooks_config.event — the CHECK covers
// handler_kind only — so a misspelled event is accepted by SQLite, never
// selected by ListByEvent, and therefore never fires. The failure is
// completely silent: the hook lists, enables, and looks healthy. That is
// why the error message enumerates every legal value rather than just
// saying "invalid": the caller who typo'd "PreToolUse" needs to see
// "pre_tool_call" without reading the source.
func ValidateEvent(e Event) error {
	for _, known := range AllEvents {
		if e == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q (valid events: %s)", ErrUnknownEvent, string(e),
		strings.Join(EventNames(), ", "))
}

// EventNames renders AllEvents as strings in the same stable order, for
// error messages, CLI help text, and API validation responses.
func EventNames() []string {
	out := make([]string, 0, len(AllEvents))
	for _, e := range AllEvents {
		out = append(out, string(e))
	}
	return out
}

// HandlerKind enumerates the three dispatch backends the platform supports.
// The hooks_config CHECK constraint enforces the same set at the schema
// level so a bad insert fails fast.
type HandlerKind string

const (
	HandlerKindShell    HandlerKind = "shell"
	HandlerKindHTTP     HandlerKind = "http"
	HandlerKindSubagent HandlerKind = "subagent"
)

// Outcome is the coarse verdict a handler returns. Block is only meaningful
// for hooks registered with Blocking=true; for non-blocking hooks a Block
// result is degraded to a logged warning.
type Outcome string

const (
	OutcomePass  Outcome = "pass"
	OutcomeBlock Outcome = "block"
	OutcomeError Outcome = "error"
)

// Hook is the in-memory form of a hooks_config row. Matcher and
// HandlerConfig are stored as JSON in SQLite; Register marshals Matcher
// from the struct and HandlerConfig from a map[string]any.
type Hook struct {
	ID            string
	WorkspaceID   string
	CrewID        string // empty means workspace-wide (applies to every crew)
	Event         Event
	Matcher       Matcher
	HandlerKind   HandlerKind
	HandlerConfig map[string]any
	Blocking      bool
	Enabled       bool
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Matcher is a JSON predicate on the event context. All fields are optional
// and AND-combined: an empty Matcher matches every event. Slices use "any
// match" semantics internally — a Tools=["Bash","Read"] matcher fires on
// either tool. Regex entries are compiled lazily in matcher.go and cached.
type Matcher struct {
	// Tools contains regex patterns matched against the tool name. When
	// empty the matcher does not constrain by tool.
	Tools []string `json:"tools,omitempty"`

	// AgentIDs is an exact-match list; an empty list means "any agent".
	AgentIDs []string `json:"agent_ids,omitempty"`

	// CrewIDs is an exact-match list layered on top of the row-level
	// crew_id scope. Useful for workspace-wide hooks that should still
	// only fire for a handful of crews.
	CrewIDs []string `json:"crew_ids,omitempty"`

	// Severities filters events that carry a severity (guardrail trips,
	// budget warnings). An empty list matches every severity.
	Severities []string `json:"severities,omitempty"`

	// When reserves a slot for a future CEL / expr predicate so we can
	// add richer filtering without another schema change. Ignored today.
	When string `json:"when,omitempty"`
}

// Result is what a handler returns to the dispatcher. Message is a short
// human-readable explanation surfaced in the journal entry; Payload is the
// full handler response that UI can render on demand.
type Result struct {
	Outcome Outcome
	Message string
	Latency time.Duration
	Payload any
}

// Errors surfaced across the package. ErrShellHookNotAllowed is returned by
// Register when a non-OWNER caller tries to create a shell hook.
// ErrSubagentHandlerNotConfigured is returned by Dispatch when a subagent
// hook fires but the orchestrator hasn't injected a handler yet.
// ErrUnknownEvent is returned by ValidateEvent (and therefore by Register /
// Update) for an event name outside AllEvents.
var (
	ErrShellHookNotAllowed          = errors.New("hooks: shell handlers require OWNER role")
	ErrSubagentHandlerNotConfigured = errors.New("hooks: subagent handler not configured")
	ErrUnknownHandlerKind           = errors.New("hooks: unknown handler kind")
	ErrUnknownEvent                 = errors.New("hooks: unknown event")
)

// BlockedError is returned by Dispatch when a blocking hook fires a Block
// outcome. The call site uses errors.As to pull it out and short-circuit
// the triggering operation (e.g. cancel the tool call, deny the memory
// write). The wrapped Result carries the handler's message.
type BlockedError struct {
	HookID string
	Event  Event
	Result Result
}

// HookBlocked lets packages that intentionally avoid importing hooks (most
// notably orchestrator) distinguish an explicit policy refusal through a
// small structural interface. It remains available through errors.Join and
// errors.As.
func (e *BlockedError) HookBlocked() bool { return true }

func (e *BlockedError) Error() string {
	if e.Result.Message != "" {
		return "hooks: blocked by " + e.HookID + ": " + e.Result.Message
	}
	return "hooks: blocked by " + e.HookID
}

// DispatchError means hook policy could not be evaluated or a configured
// handler failed to run. It is deliberately distinct from BlockedError: the
// former is an infrastructure/configuration failure, while the latter is an
// explicit policy decision. Gate call sites normally fail closed on both but
// must report them differently; observation call sites may log and continue.
type DispatchError struct {
	Event  Event
	HookID string // empty when failure happened before a hook was selected
	Cause  error
}

func (e *DispatchError) Error() string {
	if e.HookID != "" {
		return fmt.Sprintf("hooks: dispatch %s hook %s failed: %v", e.Event, e.HookID, e.Cause)
	}
	return fmt.Sprintf("hooks: dispatch %s failed: %v", e.Event, e.Cause)
}

func (e *DispatchError) Unwrap() error { return e.Cause }
