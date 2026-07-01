package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// AgentRunRequest describes everything needed to execute an agent run inside
// a container, including identity, credentials, prompts, and resource limits.
type AgentRunRequest struct {
	AgentID      string
	AgentSlug    string
	AgentRole    string // AGENT, LEAD
	CrewID       string
	CrewSlug     string
	ChatID       string
	MissionID    string // mission this run belongs to; threaded into every journal emit so Cartographer checkpoints can anchor on per-mission journal cursors.
	WorkspaceID  string
	ContainerID  string
	CLIAdapter   string // CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, FACTORY_DROID
	LLMModel     string // optional model override (e.g. claude-haiku-4-5-20251001)
	SystemPrompt string
	UserMessage  string
	ToolProfile  string // MINIMAL, CODING, FULL
	Credentials  []Credential
	TimeoutSecs  int
	// MaxTurns caps the adapter-side agent loop (via an adapter-specific
	// turn-cap flag) as defense-in-depth against a runaway that burns budget
	// re-sending context every turn. 0 means "use DefaultMaxTurns". Routine /
	// scheduled runs set RoutineMaxTurns — lower, because an unattended job
	// with no human watching is exactly where a stuck loop goes unnoticed.
	MaxTurns           int
	MemoryEnabled      bool
	CrewMembers        []CrewMember // Populated by bridge for LEAD agents
	SkipSidecar        bool         // When true, skip sidecar even if enabled globally (prevents port conflict in sub-agents)
	ApprovalMode       string       // "none" | "async" | "sync" — drives Harbor Master gate in RunAgent
	SkipConvHistory    bool         // When true, skip injecting conversation history (used by assignment sub-agents)
	NetworkMode        string       // "free" (default) or "restricted" — crew-level network policy
	AllowedDomains     []string     // Extra allowed domains for restricted mode
	MemoryMB           int
	CPUs               float64
	TTLHours           int
	MCPServers         []MCPServerConfig // Resolved MCP server configs for this agent
	CrewMCPConfigJSON  string            // Raw crew .mcp.json (merged with agent's at runtime)
	AgentMCPConfigJSON string            // Raw agent .mcp.json additions
	PreferredLanguage  string            // Workspace language (e.g. "Czech", "English")
	Skills             []SkillBundle     // Installed skills, written to per-CLI discovery paths in addition to the [SKILLS AVAILABLE] system-prompt block

	// PR-E F6 — PERSONA + per-user peer card injection. RoleTitle
	// seeds the DefaultPersona fallback when both PERSONA layers
	// are empty so the system prompt always has at least a one-line
	// role identifier. OpenedByUserID is the chat opener (chats.
	// created_by); empty for non-chat runs (routine dispatch,
	// system jobs), in which case no peer card is injected.
	RoleTitle      string
	OpenedByUserID string
}

// SkillBundle is a single agent-installed skill rendered as a SKILL.md
// file ready for materialisation into a CLI-specific discovery path
// (.claude/skills/, .agents/skills/, .cursor/rules/, etc.). Slug becomes
// the folder/filename; Content is the full SKILL.md text including
// frontmatter — already reconstructed by the resolver since we don't
// keep raw frontmatter in the DB. Vendor is informational; per-CLI
// writers don't currently namespace by vendor (would diverge from
// upstream tooling that walks flat slugs).
type SkillBundle struct {
	Slug    string
	Vendor  string
	Content string
}

// MCPServerConfig is a resolved MCP server ready for sidecar injection.
type MCPServerConfig struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Transport   string            `json:"transport"`
	Endpoint    string            `json:"endpoint,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Credential  *MCPCredential    `json:"credential,omitempty"`
}

// MCPCredential holds a decrypted credential for MCP server authentication.
type MCPCredential struct {
	PlainValue string `json:"token"`
	Type       string `json:"type"` // "bearer", "api_key", "basic"
	Header     string `json:"header,omitempty"`
}

// Credential holds a decrypted credential ready for injection into a container
// environment, with priority ordering for failover selection.
type Credential struct {
	ID         string `json:"id,omitempty"`
	EnvVarName string `json:"env_var"`
	PlainValue string `json:"value"`
	Priority   int    `json:"priority"`
	// Type is one of: AI_CLI_TOKEN, API_KEY, CLI_TOKEN, SECRET, OAUTH2,
	// USERPASS, SSH_KEY, CERTIFICATE, GENERIC_SECRET. See
	// internal/api/credentials_types.go for the closed enum.
	Type string `json:"type,omitempty"`
	// Username is the cleartext identifier half of a USERPASS credential
	// (e.g. "user@gmail.com"). Empty for all other types. Kept separate
	// from PlainValue so the env-var pair X_USERNAME / X_PASSWORD can
	// be emitted at mount time without re-parsing a JSON blob.
	Username string `json:"username,omitempty"`
}

// RunState tracks the runtime state of an active agent run, persisted in the
// state provider for crash recovery.
type RunState struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	ChatID       string    `json:"chat_id"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	ContainerID  string    `json:"container_id"`
	ExecID       string    `json:"exec_id"`
	LastActivity time.Time `json:"last_activity"`
	CredentialID string    `json:"credential_id,omitempty"`
}

// AgentEvent is a streaming event emitted during an agent run, such as text
// output, tool calls, thinking steps, or completion signals.
type AgentEvent struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Metadata  any       `json:"metadata,omitempty"`
	Timestamp time.Time `json:"ts"`
}

// EventHandler is a callback that receives streaming events during an agent run.
type EventHandler func(event AgentEvent)

// crewState tracks per-crew runtime state (activity, TTL, container).
type crewState struct {
	lastActivity time.Time
	ttl          time.Duration
	containerID  string
}

// Orchestrator manages agent execution lifecycle: building CLI commands,
// running them inside containers, streaming output, handling credential
// failover, and managing container TTLs.
// StatsRegisterFunc is an optional callback that the Orchestrator invokes
// whenever it creates or reuses a crew container. Wired from server.go to
// StatsCollector.Register so the dashboard's live resource tile can stream
// container.stats events regardless of whether the container was created
// via the direct-run path (server/routes.go handleAgentStart) or the
// mission-orchestration path (this file's GetOrCreateContainer).
type StatsRegisterFunc func(containerID, crewID, workspaceID string)

type Orchestrator struct {
	container      provider.ContainerProvider
	state          provider.StateProvider
	convStore      *conversation.Store
	scrubber       *scrubber.Scrubber
	logger         *slog.Logger
	cooldown       *CooldownManager
	sidecarEnabled bool
	keeperEnabled  bool
	ipcBaseURL     string
	ipcToken       string
	statsRegister  StatsRegisterFunc
	mu             sync.RWMutex
	accepting      bool
	crews          map[string]*crewState

	// runSem bounds concurrent agent-run exec fan-outs. RunAgent acquires a
	// token before its container.Exec fan-out (sidecar start, the mkdir/setup
	// pass, and the heavy agent CLI exec) and releases it on every exit path.
	// Without it, N concurrent RunAgent calls translate into N simultaneous
	// heavy execs against a single Docker daemon — finding P1 (HIGH) in the
	// 2026-06 security audit. The lighter git-diff path already had a cap
	// (gitDiffSem, internal/server/routes_container.go); this is the
	// ~15×-heavier run path's equivalent. Capacity is runSemCap, configurable
	// via CREWSHIP_MAX_CONCURRENT_RUNS. The token is acquired AFTER the
	// `accepting` drain check (so a draining orchestrator rejects fast without
	// consuming a slot) and with ctx-cancellation in the acquire (so a stopped
	// or timed-out run never blocks forever on a full pool — no deadlock with
	// drain).
	runSem    chan struct{}
	runSemCap int

	// tmuxCache memoizes whether each container has tmux installed. Avoids a
	// `command -v tmux` exec on every agent run (was ~50ms per call). Key is
	// the container ID; value is true if tmux was found. Invalidated when the
	// container is recreated (new ID) so stale entries are harmless.
	tmuxCacheMu sync.RWMutex
	tmuxCache   map[string]bool

	// snapshotHashCache stores the most recent container.snapshot hash
	// per container so the post-run probe can skip emitting a fresh
	// journal entry when nothing actually changed. Stale entries (after
	// container recreation) are harmless — a new container ID gets a
	// fresh slot and the first snapshot lands as expected.
	//
	// snapshotPending maps containerID -> the hash currently being emitted
	// by some goroutine. A concurrent caller with the *same* hash skips
	// (the in-flight emit dedupes for it); a caller with a *different*
	// hash falls through and emits its own — silently dropping a
	// different-hash snapshot would lose real state changes that happened
	// while a prior probe was mid-flight. The mutex covers both maps so
	// claim+publish is atomic relative to the read path.
	snapshotHashMu    sync.Mutex
	snapshotHashCache map[string]string
	snapshotPending   map[string]string

	// snapshotInFlightMu protects snapshotInFlight itself. The per-container
	// locks inside it serialize concurrent recordContainerSnapshot calls
	// for the same containerID so only the first emits while the rest
	// short-circuit on the cached hash. Without this, N goroutines that
	// all complete a run at the same instant each pass the cache check
	// before any of them stores the hash — and every goroutine emits a
	// duplicate journal entry. Different containers don't contend.
	snapshotInFlightMu sync.Mutex
	snapshotInFlight   map[string]*sync.Mutex

	// journal is the Crew Journal emitter. Nil-safe: SetJournal replaces it
	// with a no-op. Used by Crow's Nest emit points (exec.command,
	// container.metrics) so live visibility into containers flows through
	// the same append-only stream as everything else.
	journal JournalEmitter

	// hooks + approvalGate + episodicRecall are optional integration
	// points. Each is nil-safe: callers always exercise them through the
	// getter helpers which fall back to no-ops. SetHooksDispatcher /
	// SetApprovalGate / SetEpisodicRecall wire them from server.New.
	hooks          HookDispatcher
	approvalGate   ApprovalGate
	episodicRecall EpisodicRecaller
	presence       PresenceTracker
	memoryMetrics  MemoryMetricsReader
	// postToolCallObs is the optional PR-C F4.2 behavior monitor hook.
	// Wired from server.New via SetPostToolCallObserver; nil-safe via
	// the getter so a server without behaviorhook installed (e.g. dev
	// builds without ANTHROPIC_API_KEY) just no-ops on the hot path.
	postToolCallObs PostToolCallObserver
	// postToolCallSem is a bounded semaphore (channel as token bucket)
	// that caps in-flight Observe goroutines. Without this, a chatty
	// tool-call stream could fan out one goroutine per call (LLM
	// latency ~8s × call rate) and pile up before the observer's own
	// sampling gate fires. Initialised in New(); buffered to
	// postToolCallSemCap. Non-blocking send → drop policy is correct
	// here: the observer's sampling means we're already discarding
	// most events by design, and dropping the overflow is preferable
	// to back-pressuring the agent's tool-result loop.
	postToolCallSem chan struct{}
	// skillInvocationObs is the optional PR #7 skill-invocation telemetry
	// hook. Wired from server.New via SetSkillInvocationObserver; nil-safe
	// via getSkillInvocationObserver so a server without the observer
	// installed just no-ops on the hot path. Sibling to postToolCallObs:
	// both ride the same tool_call event tap.
	skillInvocationObs SkillInvocationObserver
	// skillInvocationSem bounds in-flight Observe goroutines for the skill
	// telemetry observer, identical token-bucket-with-drop policy to
	// postToolCallSem. The observer's work is a small bounded SQL txn (no
	// LLM call), so saturation is far less likely than the behavior
	// monitor's — but the cap keeps a pathological tool storm from
	// spawning unbounded goroutines all the same.
	skillInvocationSem chan struct{}
	// workspaceMemory resolves cross-crew memory for a workspace into a
	// [WORKSPACE MEMORY] system-prompt block. Nil-safe — when no
	// provider is wired (default), buildWorkspaceMemoryBlock returns
	// ("", 0) and the budget reclaims to the agent tier unchanged.
	// Wired from server.New via SetWorkspaceMemoryProvider.
	workspaceMemory WorkspaceMemoryProvider

	// convSummarizer compacts the overflow (oldest) slice of a long
	// conversation into a short summary block instead of dropping it
	// outright. Nil-safe: when unwired (no aux model configured) the
	// conversation builder falls back to plain newest-first truncation,
	// byte-for-byte unchanged. Wired from server.New via
	// SetConversationSummarizer.
	convSummarizer ConversationSummarizer

	// now is an injectable clock for resolving "today" when temporally
	// anchoring a compaction summary. Production leaves it nil and nowUTC()
	// falls back to time.Now().UTC() — the same shape as
	// consolidate.Consolidator.Now. Tests pin it to a fixed instant.
	now func() time.Time

	// episodicUnreachableLastLogged tracks when we last surfaced an
	// "ollama unreachable" log so we can dedup the spam without going
	// permanently silent. N parallel agent runs each hit recall every
	// turn; without this dedup we'd log every miss. With it we log the
	// first miss, then nothing until the suppression window elapses —
	// long enough to keep logs quiet, short enough that a *new* outage
	// after recovery still surfaces.
	episodicUnreachableLastLogged atomic.Int64 // unix nano of last log
}

// episodicUnreachableLogInterval is the minimum gap between two
// "ollama unreachable" log lines. Picked at the human-attention scale —
// short enough to flag a recurring problem within one work block, long
// enough that a stuck Ollama doesn't drown the log.
const episodicUnreachableLogInterval = 10 * time.Minute

// HookDispatcher is the narrow interface the orchestrator uses to fire
// lifecycle hook events (pre/post agent start, pre/post LLM call, etc.)
// without importing internal/hooks directly. The adapter in server/ maps
// this to the full hooks.Dispatch signature.
type HookDispatcher interface {
	Dispatch(ctx context.Context, event string, eventCtx HookEventContext) error
}

// PostToolCallObserver is the narrow interface the orchestrator uses to
// notify the PR-C F4.2 behavior monitor of each tool_call event. The
// adapter in server/ (post_tool_call_adapter.go) forwards to
// behaviorhook.Get().MaybeEvaluate — the hook itself owns sampling, LLM
// budget, and the decision-to-journal/inbox mapping. The orchestrator's
// job is just to invoke it on the hot path.
//
// Decoupled via a narrow interface (rather than direct import of
// internal/keeper/behaviorhook) so this package stays free of keeper
// dependencies — the dependency direction is one-way: server → keeper.
type PostToolCallObserver interface {
	// Observe is called synchronously from the orchestrator's tool_call
	// handler. Implementations MUST be cheap or asynchronous — the
	// orchestrator already calls Observe from a goroutine but
	// implementations should still treat the call as best-effort. Errors
	// are not returned (logged inside the observer).
	Observe(ToolCallObservation)
}

// ToolCallObservation carries the EventPostToolCall payload across the
// narrow PostToolCallObserver interface. Fields mirror hooks.EventContext
// but stay in orchestrator's vocabulary so we don't pull internal/hooks
// types into this package's exported surface.
type ToolCallObservation struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	ToolName    string
	Payload     map[string]any
}

// SkillInvocationObserver is the narrow interface the orchestrator uses
// to notify the PR #7 skill-invocation telemetry consumer of each
// tool_call event. The adapter in server/ (skill_invocation_observer.go)
// resolves the agent's assigned skill slugs, matches the tool call
// against them, and — on a hit — writes a skill_invocations audit row,
// denormalises the skills lifecycle counters, and emits a skill.invoked
// journal entry, all in one transaction.
//
// Decoupled via a narrow interface (rather than a direct import of the
// server-side SQL implementation) so this package stays free of database
// and journal dependencies on its exported surface — same one-way
// dependency direction as PostToolCallObserver (server → orchestrator).
type SkillInvocationObserver interface {
	// Observe is called from the orchestrator's tool_call event tap,
	// already inside a goroutine bounded by skillInvocationSem.
	// Implementations MUST treat the call as best-effort: errors are
	// logged inside the observer, never returned. A tool call that does
	// not correspond to one of the agent's assigned skills is a no-op.
	Observe(SkillInvocation)
}

// SkillInvocation carries the tool_call payload the skill-invocation
// observer needs to resolve + record an invocation. Mirrors
// ToolCallObservation but kept distinct so the two observers can evolve
// independently (e.g. duration/exit-code fields the behavior monitor
// doesn't care about).
type SkillInvocation struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	// ToolName is the raw tool name from the event ("Skill", "Read",
	// "Bash", or a slug). The observer decides whether it maps to an
	// assigned skill.
	ToolName string
	// Payload carries the tool input under the "input" key (the skill
	// slug for a "Skill" tool call lives there). Bounded upstream.
	Payload map[string]any
}

// HookEventContext mirrors hooks.EventContext in a narrow form so
// orchestrator stays independent of internal/hooks.
type HookEventContext struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	ToolName    string
	Severity    string
	Payload     map[string]any
}

// ApprovalGate is the narrow interface the orchestrator uses to check
// whether an action needs human approval before proceeding. The
// adapter in server/ wraps harbormaster.Gate so this package stays
// decoupled.
type ApprovalGate interface {
	Check(ctx context.Context, input ApprovalCheckInput) (ApprovalDecision, error)
}

type ApprovalCheckInput struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	Tool        string
	Args        map[string]any
	Mode        string // "none" | "async" | "sync"
	UserID      string
}

type ApprovalDecision struct {
	Required  bool
	Approved  bool
	Denied    bool
	Pending   bool
	RequestID string
	Reason    string
}

// EpisodicRecaller retrieves past similar journal entries for prompt
// injection. A nil recaller produces an empty recall — the orchestrator
// just skips the injection without erroring.
type EpisodicRecaller interface {
	Recall(ctx context.Context, input EpisodicRecallInput) (string, error)
}

type EpisodicRecallInput struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	Role        string // AGENT / LEAD
	Query       string
	MaxChars    int
}

// SetHooksDispatcher wires the hooks dispatcher. nil → no-op so
// emit sites never need to nil-check.
func (o *Orchestrator) SetHooksDispatcher(h HookDispatcher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hooks = h
}

// SetApprovalGate wires the Harbor Master approval gate.
func (o *Orchestrator) SetApprovalGate(g ApprovalGate) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.approvalGate = g
}

// SetEpisodicRecall wires episodic memory recall for prompt injection.
func (o *Orchestrator) SetEpisodicRecall(r EpisodicRecaller) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.episodicRecall = r
}

// PresenceTracker is the narrow interface the orchestrator uses to flip
// an agent's Watch Roster row (busy / online / offline) on lifecycle
// transitions. A real adapter (in server/) wraps presence.Upsert;
// the no-op fallback keeps call sites nil-free — the orchestrator
// never imports internal/presence directly (that would form a cycle
// via internal/api).
type PresenceTracker interface {
	// Track writes the new snapshot row. The implementation is
	// responsible for emitting agent.status_change only on actual
	// transitions (same-status writes are idempotent).
	Track(ctx context.Context, in PresenceInput) error
}

// PresenceInput carries the minimum the tracker needs to upsert. Status
// uses the same string values as presence.Status (online/busy/blocked/
// offline) — keeping the string form here avoids pulling the presence
// package into orchestrator's import set.
//
// MissionID is forwarded to the journal.Entry emitted by
// presence.Upsert on a real transition, keeping the mission-scoped
// timeline correct (mirrors the AgentRunRequest → JournalEntry
// MissionID threading fixed in PR #205).
type PresenceInput struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	Status      string
	Details     map[string]any
}

// SetPresenceTracker wires the Watch Roster tracker. nil is accepted
// and swapped with a no-op so emit sites can stay nil-check-free.
func (o *Orchestrator) SetPresenceTracker(p PresenceTracker) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.presence = p
}

func (o *Orchestrator) getPresence() PresenceTracker {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.presence == nil {
		return noopPresence{}
	}
	return o.presence
}

type noopPresence struct{}

func (noopPresence) Track(_ context.Context, _ PresenceInput) error { return nil }

// MemoryMetricsReader is the narrow surface the orchestrator uses to
// build the in-session nudge + cost-awareness blocks. Same adapter
// pattern as the other integration points: orchestrator stays
// decoupled from internal/journal + cost_ledger SQL; a server-side
// adapter wraps *sql.DB. Both methods are best-effort — errors
// degrade to "skip the nudge / cost line" rather than blocking a run.
type MemoryMetricsReader interface {
	// EntriesSinceLastMemoryUpdate returns how many journal entries
	// the agent has emitted since its last memory.updated entry,
	// falling back to a 30-day window when no memory.updated exists.
	EntriesSinceLastMemoryUpdate(ctx context.Context, workspaceID, agentID string) (int64, error)

	// AgentSpendLast24h returns the paymaster rollup for a single
	// agent over the last 24h. Zero counts mean no calls — caller
	// should skip the block rather than print "0 calls / 0 spent".
	AgentSpendLast24h(ctx context.Context, workspaceID, agentID string) (usd float64, tokens int64, calls int64, err error)
}

// SetMemoryMetrics wires the nudge / cost-awareness reader. nil is
// accepted and swapped with a no-op so call sites don't need a
// nil check.
func (o *Orchestrator) SetMemoryMetrics(m MemoryMetricsReader) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.memoryMetrics = m
}

func (o *Orchestrator) getMemoryMetrics() MemoryMetricsReader {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.memoryMetrics == nil {
		return noopMemoryMetrics{}
	}
	return o.memoryMetrics
}

type noopMemoryMetrics struct{}

func (noopMemoryMetrics) EntriesSinceLastMemoryUpdate(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}
func (noopMemoryMetrics) AgentSpendLast24h(_ context.Context, _, _ string) (float64, int64, int64, error) {
	return 0, 0, 0, nil
}

func (o *Orchestrator) getHooks() HookDispatcher {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.hooks == nil {
		return noopHooks{}
	}
	return o.hooks
}

func (o *Orchestrator) getApprovalGate() ApprovalGate {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.approvalGate == nil {
		return noopGate{}
	}
	return o.approvalGate
}

func (o *Orchestrator) getEpisodicRecall() EpisodicRecaller {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.episodicRecall // nil allowed; caller checks
}

// SetPostToolCallObserver wires the PR-C F4.2 behavior monitor onto the
// orchestrator hot path. nil is accepted and treated as "no observer
// configured" — the tappedHandler tool_call branch just no-ops when
// getPostToolCallObserver returns nil.
func (o *Orchestrator) SetPostToolCallObserver(obs PostToolCallObserver) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.postToolCallObs = obs
}

func (o *Orchestrator) getPostToolCallObserver() PostToolCallObserver {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.postToolCallObs // nil allowed; caller guards
}

// SetSkillInvocationObserver wires the PR #7 skill-invocation telemetry
// consumer onto the orchestrator hot path. nil is accepted and treated
// as "no observer configured" — the tool_call branch no-ops when
// getSkillInvocationObserver returns nil.
func (o *Orchestrator) SetSkillInvocationObserver(obs SkillInvocationObserver) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.skillInvocationObs = obs
}

func (o *Orchestrator) getSkillInvocationObserver() SkillInvocationObserver {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.skillInvocationObs // nil allowed; caller guards
}

// dispatchToolCallObservers fans a single tool_call event out to both the
// behavior monitor (PR-C F4.2) and the skill-invocation telemetry
// consumer (PR #7). Both ride this one tap, so the tool_name + input are
// extracted once and shared. Each observer runs in a goroutine bounded by
// its own token-bucket semaphore: a non-blocking claim spawns the
// goroutine, an overflow drops the event (and logs) rather than piling up
// unbounded goroutines under a tool storm. The behavior monitor's own
// sampling already discards most calls, and the skill consumer's work is
// a small bounded SQL txn, so a dropped overflow is acceptable on both.
//
// Extracted from RunAgent's event tap so the dispatch contract (which
// tools reach which observer, and the bounded-fan-out policy) is unit-
// testable without driving a full agent run. event MUST be a tool_call
// event; the caller guards the type check.
func (o *Orchestrator) dispatchToolCallObservers(req AgentRunRequest, event AgentEvent) {
	toolName := event.Content
	if toolName == "" {
		if m, ok := event.Metadata.(map[string]interface{}); ok {
			if tn, ok := m["tool_name"].(string); ok {
				toolName = tn
			}
		}
	}
	if toolName == "" {
		return
	}
	payload := map[string]any{}
	if m, ok := event.Metadata.(map[string]interface{}); ok {
		if in, ok := m["input"]; ok {
			payload["input"] = in
		}
	}

	if obs := o.getPostToolCallObserver(); obs != nil {
		select {
		case o.postToolCallSem <- struct{}{}:
			go func() {
				defer func() { <-o.postToolCallSem }()
				obs.Observe(ToolCallObservation{
					WorkspaceID: req.WorkspaceID,
					CrewID:      req.CrewID,
					AgentID:     req.AgentID,
					MissionID:   req.MissionID,
					ToolName:    toolName,
					Payload:     payload,
				})
			}()
		default:
			o.logger.Debug("post_tool_call: observer saturated, dropping event",
				"agent_id", req.AgentID, "tool", toolName)
		}
	}

	if obs := o.getSkillInvocationObserver(); obs != nil {
		select {
		case o.skillInvocationSem <- struct{}{}:
			go func() {
				defer func() { <-o.skillInvocationSem }()
				obs.Observe(SkillInvocation{
					WorkspaceID: req.WorkspaceID,
					CrewID:      req.CrewID,
					AgentID:     req.AgentID,
					MissionID:   req.MissionID,
					ToolName:    toolName,
					Payload:     payload,
				})
			}()
		default:
			o.logger.Debug("skill_invocation: observer saturated, dropping event",
				"agent_id", req.AgentID, "tool", toolName)
		}
	}
}

type noopHooks struct{}

func (noopHooks) Dispatch(_ context.Context, _ string, _ HookEventContext) error { return nil }

type noopGate struct{}

func (noopGate) Check(_ context.Context, _ ApprovalCheckInput) (ApprovalDecision, error) {
	return ApprovalDecision{Required: false, Approved: true}, nil
}

// JournalEmitter is a narrow interface the orchestrator uses to emit Crow's
// Nest events without importing the full journal package (avoids an import
// cycle with internal/api, which imports orchestrator).
type JournalEmitter interface {
	Emit(ctx context.Context, e JournalEntry) (string, error)
}

// JournalEntry mirrors the subset of journal.Entry fields the orchestrator
// populates. Callers should map it to journal.Entry at the boundary.
type JournalEntry struct {
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string
	Type        string
	Severity    string
	ActorType   string
	ActorID     string
	Summary     string
	Payload     map[string]any
	Refs        map[string]any
}

// SetJournal wires the journal emitter. nil is accepted and swapped with
// a no-op emitter so emit call sites never need to nil-check. Called by
// server.New after the journal writer is constructed.
func (o *Orchestrator) SetJournal(j JournalEmitter) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if j == nil {
		o.journal = noopJournal{}
		return
	}
	o.journal = j
}

// getJournal returns the configured emitter or a no-op. Safe under
// concurrent reads because SetJournal holds mu.Lock and readers use
// mu.RLock via this helper.
func (o *Orchestrator) getJournal() JournalEmitter {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.journal == nil {
		return noopJournal{}
	}
	return o.journal
}

// noopJournal is the fallback used in tests and pre-wiring code paths so
// emit calls never panic and never need an `if j != nil` guard.
type noopJournal struct{}

func (noopJournal) Emit(_ context.Context, _ JournalEntry) (string, error) {
	return "", nil
}

// truncateStr clips a string to n runes with an ellipsis, used for
// approval-gate payloads where the full user message is unnecessary
// (and potentially sensitive).
func truncateStr(s string, n int) string {
	if n <= 0 {
		return s
	}
	// Operate on runes consistently — the prior `len(s) <= n` byte
	// early-return let multi-byte UTF-8 strings shorter than n bytes
	// but longer than n runes slip through without truncation, giving
	// journal summaries unexpected width for non-ASCII content.
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// truncateCmd renders an argv slice into a single-line summary suitable for
// journal.Entry.Summary. argv is joined with spaces; the result is clipped
// to n runes with an ellipsis so the UI table row stays one line. Full
// argv lives in payload.cmd for anyone who needs the unabridged form.
func truncateCmd(argv []string, n int) string {
	s := strings.Join(argv, " ")
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// New creates an Orchestrator with the given container and state providers.
// postToolCallSemCap is the max number of concurrent behavior-monitor
// observations in flight. Sized for the worst case where every active
// crew has an agent firing tool calls in lockstep: 64 should comfortably
// cover the realistic crew count on a single instance while keeping the
// goroutine ceiling bounded. Overflow drops; the observer's sampling
// already reduces the effective rate so dropped events are statistically
// indistinguishable from un-sampled ones.
const postToolCallSemCap = 64

// skillInvocationSemCap bounds concurrent skill-invocation telemetry
// observations. The observer's work is a small bounded SQL transaction
// (no LLM latency), so it drains far faster than the behavior monitor;
// the cap exists only to keep a pathological tool storm from spawning
// unbounded goroutines. Overflow drops the telemetry event — the same
// statistically-acceptable loss policy as the behavior observer.
const skillInvocationSemCap = 64

// defaultRunSemCap bounds concurrent agent-run exec fan-outs when the
// CREWSHIP_MAX_CONCURRENT_RUNS env override is unset or invalid. The run path
// is far heavier than the git-diff path (gitDiffSem=4): each run fans out a
// sidecar start, ~6 setup execs, and a long-lived agent CLI exec, all against
// one Docker daemon. 8 trades a little queueing latency under burst for
// protection against daemon saturation / host OOM (finding P1).
const defaultRunSemCap = 8

// resolveRunSemCap reads CREWSHIP_MAX_CONCURRENT_RUNS, falling back to
// defaultRunSemCap when unset, non-numeric, or non-positive.
func resolveRunSemCap() int {
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_MAX_CONCURRENT_RUNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultRunSemCap
}

// acquireRunSlot blocks until a run-concurrency token is free or ctx is
// cancelled. The returned release func must be called exactly once on every
// exit path (it is idempotent). A nil runSem (constructed outside New) yields
// an immediate no-op release so the run path never deadlocks on an
// uninitialised orchestrator.
func (o *Orchestrator) acquireRunSlot(ctx context.Context) (func(), error) {
	if o.runSem == nil {
		return func() {}, nil
	}
	select {
	case o.runSem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-o.runSem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func New(
	container provider.ContainerProvider,
	state provider.StateProvider,
	logger *slog.Logger,
) *Orchestrator {
	runSemCap := resolveRunSemCap()
	return &Orchestrator{
		container:          container,
		state:              state,
		scrubber:           scrubber.New(),
		logger:             logger,
		cooldown:           NewCooldownManager(),
		accepting:          true,
		crews:              make(map[string]*crewState),
		tmuxCache:          make(map[string]bool),
		snapshotHashCache:  make(map[string]string),
		snapshotPending:    make(map[string]string),
		snapshotInFlight:   make(map[string]*sync.Mutex),
		postToolCallSem:    make(chan struct{}, postToolCallSemCap),
		skillInvocationSem: make(chan struct{}, skillInvocationSemCap),
		runSem:             make(chan struct{}, runSemCap),
		runSemCap:          runSemCap,
	}
}

// SetStatsRegisterCallback wires a callback invoked on every crew container
// create/reuse so the stats collector can start polling and broadcasting
// container.stats WS events. Called once at server bootstrap.
func (o *Orchestrator) SetStatsRegisterCallback(fn StatsRegisterFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statsRegister = fn
}

// RegisterStatsContainer hands a container off to the stats collector for
// metric polling + container.metrics journal emit. Safe to call repeatedly
// for the same container (the collector dedupes). No-op when the callback
// hasn't been wired (tests / dry-run) or workspaceID is empty.
//
// chat-driven runs that go through chatbridge call EnsureCrewRuntime
// directly (they need extra CrewConfig fields like PostStartCommands that
// GetOrCreateContainer doesn't accept) — they MUST also call this so
// Crow's Nest's Resources panel actually fills with data.
func (o *Orchestrator) RegisterStatsContainer(containerID, crewID, workspaceID string) {
	if containerID == "" || workspaceID == "" {
		return
	}
	o.mu.RLock()
	reg := o.statsRegister
	o.mu.RUnlock()
	if reg != nil {
		reg(containerID, crewID, workspaceID)
	}
}

// SetWorkspaceMemoryProvider wires the cross-crew memory tier into
// buildMemoryContext. Until this is called, agents only see their own
// memory + their crew's shared memory + pins. Passing nil disables
// the tier (test convenience). Concurrency-safe; can be re-wired
// after construction.
func (o *Orchestrator) SetWorkspaceMemoryProvider(p WorkspaceMemoryProvider) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.workspaceMemory = p
}

// ConversationSummarizer compacts a block of older conversation turns into
// a short prose summary. It is deliberately the same single-method shape as
// the consolidator's summarizer slot so the existing aux-LLM adapter wired
// in server.New satisfies it by structural match — the orchestrator never
// imports internal/consolidate (that would form a dependency cycle).
type ConversationSummarizer interface {
	Summarize(ctx context.Context, prompt string) (string, error)
}

// SetConversationSummarizer wires the aux-LLM slot used to compact the
// overflow slice of a long conversation. Passing nil (the default until an
// aux model is configured) keeps buildConversationContext on its plain
// newest-first truncation path. Concurrency-safe; can be re-wired after
// construction.
func (o *Orchestrator) SetConversationSummarizer(s ConversationSummarizer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.convSummarizer = s
}

// getConvSummarizer returns the wired conversation summarizer or nil. Unlike
// getHooks/getEpisodicRecall there is no no-op fallback object: a nil result
// is the explicit signal for the truncation fallback path.
func (o *Orchestrator) getConvSummarizer() ConversationSummarizer {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.convSummarizer
}

// nowUTC resolves the current time in UTC, using the injectable o.now clock
// when set (tests pin it) and falling back to time.Now().UTC() otherwise. It
// always normalizes to UTC so date formatting is stable regardless of the
// injected zone — mirroring consolidate.Consolidator.now().
func (o *Orchestrator) nowUTC() time.Time {
	if o.now != nil {
		return o.now().UTC()
	}
	return time.Now().UTC()
}

// SetSidecarEnabled enables the sidecar proxy for credential injection.
// When enabled, credentials are NOT passed via env vars. Instead, a sidecar
// proxy is started inside the container that intercepts HTTP requests and
// injects the appropriate API keys.
func (o *Orchestrator) SetSidecarEnabled(enabled bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sidecarEnabled = enabled
}

// SetKeeperEnabled enables or disables the Keeper AI assistant for agent runs.
func (o *Orchestrator) SetKeeperEnabled(enabled bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.keeperEnabled = enabled
}

// KeeperEnabled reports whether the Keeper AI assistant is enabled.
func (o *Orchestrator) KeeperEnabled() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.keeperEnabled
}

// SetIPCConfig sets the crewshipd internal API base URL and token.
// The sidecar uses this to forward assignment requests from lead agents back to crewshipd.
// baseURL should be reachable from inside the Docker container (e.g. http://host.docker.internal:8080).
func (o *Orchestrator) SetIPCConfig(baseURL, token string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ipcBaseURL = baseURL
	o.ipcToken = token
}

// ContainerProvider returns the underlying container provider.
// Used by the Keeper execute handler to run commands inside agent containers.
func (o *Orchestrator) ContainerProvider() provider.ContainerProvider {
	return o.container
}

// GetOrCreateContainer returns the container ID for a crew, creating it if it doesn't exist.
// Used by assignment goroutines to ensure the crew container is running before exec-ing the sub-agent.
//
// workspaceID is passed through to the stats-register callback so the dashboard's
// container resources tile can scope its WS stream correctly. Pass empty string
// if called from a context where workspace is unknown (the register callback
// short-circuits on empty workspace).
