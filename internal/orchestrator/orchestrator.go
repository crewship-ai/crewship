package orchestrator

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// AgentRunRequest describes everything needed to execute an agent run inside
// a container, including identity, credentials, prompts, and resource limits.
type AgentRunRequest struct {
	AgentID            string
	AgentSlug          string
	AgentRole          string // AGENT, LEAD, COORDINATOR (deprecated — see docs/guides/coordinator.mdx)
	CrewID             string
	CrewSlug           string
	ChatID             string
	MissionID          string // mission this run belongs to; threaded into every journal emit so Cartographer checkpoints can anchor on per-mission journal cursors.
	WorkspaceID        string
	ContainerID        string
	CLIAdapter         string // CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI
	LLMModel           string // optional model override (e.g. claude-haiku-4-5-20251001)
	SystemPrompt       string
	UserMessage        string
	ToolProfile        string // MINIMAL, CODING, MESSAGING, FULL
	Credentials        []Credential
	TimeoutSecs        int
	MemoryEnabled      bool
	CrewMembers        []CrewMember     // Populated by bridge for LEAD agents
	AllCrews           []CrewInfo       // Deprecated: COORDINATOR role is deprecated; see [BuildCoordinatorContext].
	ActiveMissions     []MissionSummary // Deprecated: COORDINATOR role is deprecated; see [BuildCoordinatorContext].
	SkipSidecar        bool             // When true, skip sidecar even if enabled globally (prevents port conflict in sub-agents)
	ApprovalMode       string           // "none" | "async" | "sync" — drives Harbor Master gate in RunAgent
	SkipConvHistory    bool             // When true, skip injecting conversation history (used by assignment sub-agents)
	NetworkMode        string           // "free" (default) or "restricted" — crew-level network policy
	AllowedDomains     []string         // Extra allowed domains for restricted mode
	MemoryMB           int
	CPUs               float64
	TTLHours           int
	MCPServers         []MCPServerConfig // Resolved MCP server configs for this agent
	CrewMCPConfigJSON  string            // Raw crew .mcp.json (merged with agent's at runtime)
	AgentMCPConfigJSON string            // Raw agent .mcp.json additions
	PreferredLanguage  string            // Workspace language (e.g. "Czech", "English")
	WorkspaceMemPath   string            // Deprecated: used only by COORDINATOR role; see [BuildCoordinatorContext].
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
	Type       string `json:"type,omitempty"` // API_KEY, AI_CLI_TOKEN, SECRET
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
	// snapshotPending tracks containers where one goroutine has claimed
	// the emit slot but hasn't completed yet. Concurrent goroutines that
	// see a containerID in this set skip work entirely (a single in-flight
	// emit is enough). The mutex covers both maps so claim+publish is
	// atomic relative to the read path.
	snapshotHashMu    sync.Mutex
	snapshotHashCache map[string]string
	snapshotPending   map[string]bool

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
}

// HookDispatcher is the narrow interface the orchestrator uses to fire
// lifecycle hook events (pre/post agent start, pre/post LLM call, etc.)
// without importing internal/hooks directly. The adapter in server/ maps
// this to the full hooks.Dispatch signature.
type HookDispatcher interface {
	Dispatch(ctx context.Context, event string, eventCtx HookEventContext) error
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
	Role        string // AGENT / LEAD / COORDINATOR
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
func New(
	container provider.ContainerProvider,
	state provider.StateProvider,
	logger *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		container:         container,
		state:             state,
		scrubber:          scrubber.New(),
		logger:            logger,
		cooldown:          NewCooldownManager(),
		accepting:         true,
		crews:             make(map[string]*crewState),
		tmuxCache:         make(map[string]bool),
		snapshotHashCache: make(map[string]string),
		snapshotPending:   make(map[string]bool),
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
