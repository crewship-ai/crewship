package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
	"github.com/crewship-ai/crewship/internal/tokenutil"
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

	// journal is the Crew Journal emitter. Nil-safe: SetJournal replaces it
	// with a no-op. Used by Crow's Nest emit points (exec.command,
	// container.metrics) so live visibility into containers flows through
	// the same append-only stream as everything else.
	journal JournalEmitter
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
		container: container,
		state:     state,
		scrubber:  scrubber.New(),
		logger:    logger,
		cooldown:  NewCooldownManager(),
		accepting: true,
		crews:     make(map[string]*crewState),
		tmuxCache: make(map[string]bool),
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
func (o *Orchestrator) GetOrCreateContainer(ctx context.Context, crewSlug, crewID, workspaceID string) (string, error) {
	if o.container == nil {
		return "", fmt.Errorf("container provider not configured")
	}
	containerID, err := o.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:   crewID,
		Slug: crewSlug,
	})
	if err != nil {
		return "", fmt.Errorf("ensure crew runtime for crew %s (workspace %s): %w", crewID, workspaceID, err)
	}
	// Register for stats streaming. Without this, the direct-run path (server
	// routes.go handleAgentStart) is the only thing that registers containers,
	// which means mission-driven runs (the overwhelming majority) produce no
	// container.stats WS events and the dashboard tile stays empty.
	o.mu.RLock()
	reg := o.statsRegister
	o.mu.RUnlock()
	if reg != nil && workspaceID != "" {
		reg(containerID, crewID, workspaceID)
	}
	return containerID, nil
}

// RunAgentForAssignment runs a sub-agent as part of a mission assignment.
// It skips conversation history injection (each task gets a clean context via the mission brief).
// SkipSidecar is respected from the caller — regular AGENT tasks skip sidecar,
// while LEAD planning tasks need sidecar for mission management API access.
func (o *Orchestrator) RunAgentForAssignment(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	req.SkipConvHistory = true
	return o.RunAgent(ctx, req, handler)
}

// SetConversationStore sets the conversation store for reading session history.
func (o *Orchestrator) SetConversationStore(store *conversation.Store) {
	o.convStore = store
}

// RunAgent executes an agent run inside its crew's container, streaming events
// to handler. It manages credential injection, output parsing, failover on
// rate limits, and run state persistence.
func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	o.mu.RLock()
	if !o.accepting {
		o.mu.RUnlock()
		return fmt.Errorf("orchestrator not accepting new runs")
	}
	o.mu.RUnlock()

	if req.ContainerID != "" {
		o.refreshActivity(req.CrewID, req.ContainerID, req.TTLHours)
		defer o.refreshActivity(req.CrewID, req.ContainerID, req.TTLHours)
	}

	runState := RunState{
		ID:          req.ChatID,
		AgentID:     req.AgentID,
		ChatID:      req.ChatID,
		Status:      "running",
		StartedAt:   time.Now(),
		ContainerID: req.ContainerID,
	}

	cred := o.selectCredential(req.Credentials)
	if cred != nil {
		runState.CredentialID = cred.ID
	}

	stateBytes, _ := json.Marshal(runState)
	if err := o.state.Set(ctx, "agent_runs", runState.ID, stateBytes); err != nil {
		o.logger.Error("failed to persist run state", "error", err)
	}

	// Inject conversation history into system prompt for context continuity.
	// Uses token-budget allocation: 60% conversation, 40% memory (of remaining budget).
	baseTokens := tokenutil.EstimateTokens(req.SystemPrompt)
	remaining := tokenutil.MaxSystemPromptTokens - baseTokens
	if remaining < 2000 {
		remaining = 2000 // minimum fallback
	}
	convTokenBudget := remaining * tokenutil.ConversationBudgetPct / 100
	memTokenBudget := remaining * tokenutil.MemoryBudgetPct / 100

	if o.convStore != nil && req.ChatID != "" && !req.SkipConvHistory {
		history := o.buildConversationContext(ctx, req.ChatID, convTokenBudget)
		if history != "" {
			req.SystemPrompt = req.SystemPrompt + "\n\n" + history
		}
	}

	// Validate slug BEFORE using it in path construction (memory context, output dirs)
	if !validSlugRe.MatchString(req.AgentSlug) || req.AgentSlug != path.Base(req.AgentSlug) {
		return fmt.Errorf("invalid agent slug: %q", req.AgentSlug)
	}

	// Assemble the final system prompt in a single strings.Builder pass.
	// The previous `systemPrompt = systemPrompt + "\n\n" + section` chain was
	// O(n²) — each step copied the full accumulated prompt, which is 5–15 kB
	// in realistic workloads.
	var promptBuf strings.Builder
	promptBuf.Grow(len(req.SystemPrompt) + 8192) // headroom for up to 4 contexts
	promptBuf.WriteString(req.SystemPrompt)

	// Inject lead crew context into system prompt (before memory, after conversation history)
	if req.AgentRole == "LEAD" && len(req.CrewMembers) > 0 {
		if leadCtx := BuildLeadContext(req.CrewMembers); leadCtx != "" {
			promptBuf.WriteString("\n\n")
			promptBuf.WriteString(leadCtx)
		}
	}

	// Inject coordinator context listing all workspace crews.
	// Deprecated: COORDINATOR role is deprecated; see [BuildCoordinatorContext].
	// Branch retained for backward compat so existing COORDINATOR agents keep working.
	if req.AgentRole == "COORDINATOR" && len(req.AllCrews) > 0 {
		if coordCtx := BuildCoordinatorContext(req.AllCrews, req.ActiveMissions); coordCtx != "" {
			promptBuf.WriteString("\n\n")
			promptBuf.WriteString(coordCtx)
		}
	}

	// Inject peer communication context for non-LEAD agents in a crew
	if req.AgentRole != "LEAD" && len(req.CrewMembers) > 0 {
		if peerCtx := BuildPeerContext(req.CrewMembers, req.AgentSlug); peerCtx != "" {
			promptBuf.WriteString("\n\n")
			promptBuf.WriteString(peerCtx)
		}
	}

	// Inject agent memory context into system prompt (after conversation history)
	if req.MemoryEnabled {
		if memoryCtx := o.buildMemoryContext(ctx, req, tokenutil.CharsForTokens(memTokenBudget)); memoryCtx != "" {
			promptBuf.WriteString("\n\n")
			promptBuf.WriteString(memoryCtx)
		}
	}

	// Inject workspace language preference so agents respond in the right language
	if req.PreferredLanguage != "" {
		promptBuf.WriteString("\n\n[LANGUAGE]\nAlways respond and write comments in ")
		promptBuf.WriteString(req.PreferredLanguage)
		promptBuf.WriteString(". All your output, summaries, and handoff descriptions must be in ")
		promptBuf.WriteString(req.PreferredLanguage)
		promptBuf.WriteString(".\n[END LANGUAGE]")
	}

	req.SystemPrompt = promptBuf.String()

	o.logger.Info("system prompt assembled",
		"agent", req.AgentSlug,
		"est_tokens", tokenutil.EstimateTokens(req.SystemPrompt),
	)

	o.mu.RLock()
	sidecarEnabled := o.sidecarEnabled && !req.SkipSidecar
	keeperEnabled := o.keeperEnabled
	ipcBaseURL := o.ipcBaseURL
	ipcToken := o.ipcToken
	o.mu.RUnlock()

	var env []string
	if sidecarEnabled {
		env = BuildEnvVarsSidecar(req, keeperEnabled)
		o.logger.Info("sidecar proxy starting", "agent_id", req.AgentID)
		var memoryCfg *SidecarMemoryConfig
		if req.MemoryEnabled {
			memoryCfg = &SidecarMemoryConfig{
				Enabled:   true,
				BasePath:  path.Join("/crew", "agents", req.AgentSlug, ".memory"),
				AgentSlug: req.AgentSlug,
				AgentRole: strings.ToLower(req.AgentRole),
			}
			// Lead agents own the crew shared memory FTS5 index
			if req.CrewID != "" {
				memoryCfg.CrewMemoryPath = "/crew/shared/.memory"
			}
		}
		// Build IPC config for agents in a crew so the sidecar can forward
		// assignment requests (LEAD), peer queries, and escalations (all roles)
		var ipcCfg *SidecarIPCConfig
		// COORDINATOR branch is deprecated (see [BuildCoordinatorContext]); retained for backward compat.
		if ipcBaseURL != "" && (req.AgentRole == "LEAD" || req.AgentRole == "COORDINATOR" || len(req.CrewMembers) > 0) {
			ipcCfg = &SidecarIPCConfig{
				BaseURL:     ipcBaseURL,
				Token:       ipcToken,
				AgentID:     req.AgentID,
				AgentSlug:   req.AgentSlug,
				CrewID:      req.CrewID,
				WorkspaceID: req.WorkspaceID,
				ChatID:      req.ChatID,
				ContainerID: req.ContainerID,
			}
		}
		// Convert crew members to sidecar format for target validation
		var sidecarMembers []SidecarCrewMember
		for _, m := range req.CrewMembers {
			sidecarMembers = append(sidecarMembers, SidecarCrewMember{
				ID:        m.ID,
				Slug:      m.Slug,
				Name:      m.Name,
				RoleTitle: m.RoleTitle,
				ChatID:    m.ChatID,
			})
		}
		// Build network policy for sidecar.
		// Normalize and validate: only "free" and "restricted" are accepted.
		desiredMode := strings.TrimSpace(strings.ToLower(req.NetworkMode))
		if desiredMode == "" {
			desiredMode = "free"
		}
		var networkPolicy *SidecarNetworkPolicy
		switch desiredMode {
		case "free":
			networkPolicy = &SidecarNetworkPolicy{Mode: "free"}
		case "restricted":
			// Auto-add API domains for stdio MCP servers so their HTTP
			// calls can pass through the sidecar proxy.
			domains := append([]string{}, req.AllowedDomains...)
			domains = append(domains, mcpStdioDomains(req.MCPServers)...)
			networkPolicy = &SidecarNetworkPolicy{
				Mode:           "restricted",
				AllowedDomains: domains,
			}
		default:
			o.logger.Error("unknown network mode, refusing to start sidecar", "mode", req.NetworkMode)
			o.updateRunStatus(ctx, runState.ID, "error")
			return fmt.Errorf("unknown network mode: %s", req.NetworkMode)
		}
		// Check if sidecar already running in this container (shared crew container).
		// Multiple agents in the same crew share one container — only the first starts the sidecar.
		// Also verify the running sidecar's network mode matches the desired mode;
		// if it differs (e.g. after a policy change), we must restart the sidecar.
		needStart := true
		if health := checkSidecar(ctx, o.container, req.ContainerID); health != nil {
			if health.NetworkMode == desiredMode && desiredMode != "restricted" {
				// In "free" mode we can safely reuse. In "restricted" mode the
				// domain allowlist may differ between agents (different MCP servers),
				// so we always restart to pick up the latest set.
				o.logger.Info("sidecar already running, reusing", "agent_id", req.AgentID, "container_id", req.ContainerID[:min(12, len(req.ContainerID))])
				needStart = false
			} else {
				o.logger.Warn("sidecar running with stale network policy, restarting",
					"running_mode", health.NetworkMode, "desired_mode", desiredMode)
				// Kill existing sidecar so we can start a new one
				_, _ = o.container.Exec(ctx, provider.ExecConfig{
					ContainerID: req.ContainerID,
					Cmd:         []string{"sh", "-c", "pkill -f crewship-sidecar || true"},
					User:        "0:0",
				})
			}
		}
		if needStart {
			if err := startSidecar(ctx, o.container, req.ContainerID, req.Credentials, memoryCfg, ipcCfg, sidecarMembers, networkPolicy, req.MCPServers, o.logger); err != nil {
				o.logger.Error("failed to start sidecar", "error", err, "agent_id", req.AgentID)
				o.updateRunStatus(ctx, runState.ID, "error")
				return fmt.Errorf("start sidecar: %w", err)
			}
		}
		credCount := 0
		for _, c := range req.Credentials {
			if credTypeToProvider(c) != "" {
				credCount++
			}
		}
		o.logger.Info("sidecar ready", "agent_id", req.AgentID, "credentials", credCount)

		// MCP servers configured via .mcp.json use ${ENV_VAR} references that
		// Claude Code expands from the process environment. With sidecar enabled
		// credentials normally skip env vars (they go via stdin instead), but
		// MCP env references still need the actual values in the exec env.
		env = injectMCPCredentialEnvVars(req, env)
	} else {
		env = BuildEnvVars(req, cred)
	}

	// Log auth mode for debugging
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			o.logger.Info("agent auth mode: OAuth (CONNECT tunnel)", "agent_id", req.AgentID)
		}
		if strings.HasPrefix(e, "ANTHROPIC_BASE_URL=") {
			o.logger.Info("agent auth mode: API key (reverse proxy)", "agent_id", req.AgentID)
		}
	}

	cmd := BuildCLICommand(req)

	scratchDir := path.Join("/workspace", req.AgentSlug)
	outputDir := path.Join("/output", req.AgentSlug)
	workDir := outputDir // CWD = output dir so files are immediately visible to user

	crewAgentDir := path.Join("/crew", "agents", req.AgentSlug)
	crewSharedDir := "/crew/shared"

	secretsAgentDir := path.Join("/secrets", req.AgentSlug)
	secretsSharedDir := "/secrets/shared"

	// Create scratch, output, per-agent crew, and secrets directories
	mkdirCfg := provider.ExecConfig{
		ContainerID: req.ContainerID,
		Cmd:         []string{"mkdir", "-p", scratchDir, outputDir, crewAgentDir, crewSharedDir, secretsAgentDir, secretsSharedDir},
		User:        "1001:1001",
	}
	mkResult, err := o.container.Exec(ctx, mkdirCfg)
	if err != nil {
		o.logger.Warn("failed to create agent dirs", "error", err)
	} else {
		io.Copy(io.Discard, mkResult.Reader)
		mkResult.Reader.Close()
	}

	// Pre-create /crew/manifest.json writable by both agent (1001) and sidecar (1002).
	manifestCfg := provider.ExecConfig{
		ContainerID: req.ContainerID,
		Cmd:         []string{"sh", "-c", `test -f /crew/manifest.json || echo '{"version":1,"packages":{"apt":[],"npm":[],"pip":[]},"credentials":[],"setup_commands":[]}' > /crew/manifest.json; chmod 0666 /crew/manifest.json`},
		User:        "0:0",
	}
	mfResult, err := o.container.Exec(ctx, manifestCfg)
	if err != nil {
		o.logger.Debug("manifest pre-create skipped", "error", err)
	} else {
		io.Copy(io.Discard, mfResult.Reader)
		mfResult.Reader.Close()
	}

	// Create .memory/ directories for persistent agent memory (in crew HOME)
	if req.MemoryEnabled {
		memoryDir := path.Join(crewAgentDir, ".memory")
		memoryDailyDir := path.Join(memoryDir, "daily")
		memorySnapshotsDir := path.Join(memoryDir, ".snapshots")
		mkMemCfg := provider.ExecConfig{
			ContainerID: req.ContainerID,
			Cmd:         []string{"mkdir", "-p", memoryDir, memoryDailyDir, memorySnapshotsDir},
			User:        "1001:1001",
		}
		mkMemResult, err := o.container.Exec(ctx, mkMemCfg)
		if err != nil {
			o.logger.Warn("failed to create memory dirs", "error", err)
		} else {
			io.Copy(io.Discard, mkMemResult.Reader)
			mkMemResult.Reader.Close()
		}

		// Create crew shared memory dirs for lead agents (if in a crew)
		if req.CrewID != "" {
			crewMemDir := "/crew/shared/.memory"
			crewMemDailyDir := path.Join(crewMemDir, "daily")
			crewMemTopicsDir := path.Join(crewMemDir, "topics")
			mkCrewMemCfg := provider.ExecConfig{
				ContainerID: req.ContainerID,
				Cmd:         []string{"mkdir", "-p", crewMemDir, crewMemDailyDir, crewMemTopicsDir},
				User:        "1001:1001",
			}
			mkCrewMemResult, err := o.container.Exec(ctx, mkCrewMemCfg)
			if err != nil {
				o.logger.Warn("failed to create crew memory dirs", "error", err)
			} else {
				io.Copy(io.Discard, mkCrewMemResult.Reader)
				mkCrewMemResult.Reader.Close()
			}
		}

		// One-time migration: copy memory from old location (/output/{slug}/.memory/)
		// to new location (/crew/agents/{slug}/.memory/) if not already migrated
		oldMemoryDir := path.Join(outputDir, ".memory")
		migScript := fmt.Sprintf(
			`if [ -d %[1]s ] && [ -z "$(ls -A %[2]s 2>/dev/null)" ]; then cp -a %[1]s/. %[2]s/ 2>/dev/null; fi; true`,
			oldMemoryDir, memoryDir,
		)
		migCfg := provider.ExecConfig{
			ContainerID: req.ContainerID,
			Cmd:         []string{"sh", "-c", migScript},
			User:        "1001:1001",
		}
		migResult, err := o.container.Exec(ctx, migCfg)
		if err != nil {
			o.logger.Debug("memory migration skipped", "error", err)
		} else {
			io.Copy(io.Discard, migResult.Reader)
			migResult.Reader.Close()
		}
	}

	// Create per-agent secrets directory and write credential files.
	// Files are written as root (UID 0) with ownership 1001:1001 and mode 0400
	// so the agent can read but not modify them.
	if err := writeCredentialFiles(ctx, o.container, req.ContainerID, req.AgentSlug, req.Credentials, secretsAgentDir, secretsSharedDir, o.logger); err != nil {
		o.logger.Warn("failed to write credential files", "error", err, "agent_id", req.AgentID)
	}
	env = append(env, "CREWSHIP_SECRETS_DIR="+secretsAgentDir)

	env = append(env, "CREWSHIP_OUTPUT_DIR="+outputDir)

	// Write non-secret Claude config (skip onboarding). Credentials are
	// also available as files in /secrets/{agent-slug}/ for CLI tools.
	if err := setupClaudeConfig(ctx, o.container, req.ContainerID, req.AgentSlug, o.logger); err != nil {
		o.logger.Warn("failed to inject claude config", "error", err, "agent_id", req.AgentID)
	}

	// Write MCP server configuration file.
	// Primary path: merge crew + agent raw .mcp.json configs (new simplified model).
	// Fallback: build from resolved MCPServerConfig entries (legacy per-binding model).
	if err := setupMCPConfig(ctx, o.container, req.ContainerID, req.AgentSlug, req.CrewMCPConfigJSON, req.AgentMCPConfigJSON, req.MCPServers, o.logger); err != nil {
		hasMCP := req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" || len(req.MCPServers) > 0
		if hasMCP {
			o.updateRunStatus(ctx, runState.ID, "error")
			return fmt.Errorf("inject MCP config: %w", err)
		}
		o.logger.Warn("failed to inject MCP config", "error", err, "agent_id", req.AgentID)
	}

	// Inject OAuth token files for MCP servers that need them.
	// When Crewship holds access+refresh tokens from OAuth flow, write them
	// to the location MCP servers expect (e.g. ~/.config/<server>/tokens.json).
	if err := injectMCPOAuthTokens(ctx, o.container, req.ContainerID, req.AgentSlug, req.MCPServers, req.Credentials, o.logger); err != nil {
		o.logger.Warn("failed to inject MCP OAuth tokens", "error", err, "agent_id", req.AgentID)
	}

	// Write CLI-specific system prompt files (e.g. AGENTS.md for OpenCode)
	if err := setupSystemPromptFiles(ctx, o.container, req.ContainerID, req, workDir, o.logger); err != nil {
		o.logger.Warn("failed to write system prompt files", "error", err, "agent_id", req.AgentID, "cli_adapter", req.CLIAdapter)
	}

	// Wrap agent CLI command with stdbuf to force line-buffered stdout.
	// Apple's container runtime buffers exec output which causes choppy
	// streaming in chat. stdbuf -oL flushes on every newline so JSON
	// stream events arrive immediately.
	//
	// When tmux is available, wrap the command inside a named tmux session
	// so users can attach via the web terminal to observe the running agent.
	// The tmux session is named "agent-{slug}" and stdout still flows through
	// the exec pipe for chat streaming.
	execCmd, tmuxErr := o.setupTmuxExec(ctx, req.ContainerID, cmd, req.AgentSlug, env)
	if tmuxErr != nil {
		o.logger.Warn("tmux setup failed, falling back to direct exec", "error", tmuxErr)
		execCmd = append([]string{"stdbuf", "-oL"}, cmd...)
	}

	execCfg := provider.ExecConfig{
		ContainerID: req.ContainerID,
		Cmd:         execCmd,
		Env:         env,
		WorkingDir:  workDir,
		User:        "1001:1001",
	}

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cIDShort := req.ContainerID
	if len(cIDShort) > 12 {
		cIDShort = cIDShort[:12]
	}
	o.logger.Info("exec agent", "agent_id", req.AgentID, "container_id", cIDShort, "cmd", cmd)

	// Crow's Nest: emit the command start so the live terminal UI can
	// open a new block before any output streams. Payload carries the
	// argv for the UI and the container ID + agent scope; full stdout
	// is streamed separately by the existing handler pipeline.
	execStart := time.Now()
	j := o.getJournal()
	_, _ = j.Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "exec.command",
		Severity:    "info",
		ActorType:   "agent",
		ActorID:     req.AgentID,
		Summary:     fmt.Sprintf("%s runs %s", req.AgentSlug, truncateCmd(cmd, 120)),
		Payload: map[string]any{
			"cmd":          cmd,
			"container_id": cIDShort,
			"adapter":      req.CLIAdapter,
			"model":        req.LLMModel,
			"phase":        "start",
		},
		Refs: map[string]any{"chat_id": req.ChatID, "container_id": req.ContainerID},
	})
	// Flip agent to busy for the Watch Roster. The presence sweeper
	// reverts to offline after idle timeout if the agent crashes before
	// the matching "online" emit at end-of-run fires.
	_, _ = j.Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "agent.status_change",
		Severity:    "info",
		ActorType:   "system",
		Summary:     fmt.Sprintf("agent %s: busy", req.AgentSlug),
		Payload:     map[string]any{"status": "busy", "current_chat_id": req.ChatID},
	})

	result, err := o.container.Exec(execCtx, execCfg)
	if err != nil {
		o.logger.Error("exec agent failed", "error", err, "agent_id", req.AgentID)
		o.updateRunStatus(ctx, runState.ID, "error")
		// Emit the terminal exec.command event for the failure path
		// too so Crow's Nest doesn't show a hanging "running" block
		// when Docker exec create/attach fails before the command runs.
		_, _ = j.Emit(ctx, JournalEntry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			Type:        "exec.command",
			Severity:    "error",
			ActorType:   "agent",
			ActorID:     req.AgentID,
			Summary:     fmt.Sprintf("%s exec FAILED: %v", req.AgentSlug, err),
			Payload: map[string]any{
				"cmd":         cmd,
				"phase":       "end",
				"error":       err.Error(),
				"duration_ms": time.Since(execStart).Milliseconds(),
			},
		})
		return fmt.Errorf("exec agent: %w", err)
	}

	// Wrap handler with credential scrubbing to prevent secret leakage
	// in agent output (prompt injection defense).
	scrubHandler := o.wrapScrubHandler(handler)
	o.streamOutput(execCtx, result, req, scrubHandler)

	// If context was cancelled (user pressed stop), clean up with a fresh
	// context and return a cancellation error. The reader close in streamOutput
	// sends SIGPIPE to the exec process, which should terminate it.
	if ctx.Err() != nil {
		cleanCtx := context.Background()
		o.updateRunStatus(cleanCtx, runState.ID, "cancelled")
		o.logger.Info("run cancelled", "agent_id", req.AgentID, "exec_id", result.ExecID)
		return fmt.Errorf("run cancelled: %w", ctx.Err())
	}

	running, exitCode, _ := o.container.ExecInspect(ctx, result.ExecID)
	o.logger.Info("exec finished", "agent_id", req.AgentID, "running", running, "exit_code", exitCode)

	// Crow's Nest: closing exec.command entry with exit code + duration
	// so the live terminal UI can mark the block done and the dashboard
	// can tally success/failure rates. Severity switches to warn when
	// the command exited non-zero — the default warn+ filter surfaces
	// failures without consumers having to parse payload.
	endSeverity := "info"
	if exitCode != 0 {
		endSeverity = "warn"
	}
	_, _ = j.Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "exec.command",
		Severity:    endSeverity,
		ActorType:   "agent",
		ActorID:     req.AgentID,
		Summary:     fmt.Sprintf("%s: exit %d (%dms)", req.AgentSlug, exitCode, time.Since(execStart).Milliseconds()),
		Payload: map[string]any{
			"cmd":         cmd,
			"phase":       "end",
			"exit_code":   exitCode,
			"duration_ms": time.Since(execStart).Milliseconds(),
			"running":     running,
		},
		Refs: map[string]any{"chat_id": req.ChatID, "exec_id": result.ExecID},
	})
	// Flip agent back to online for the Watch Roster now that the run
	// is done. If the agent stays in-session, the presence sweeper
	// still tracks idleness separately.
	_, _ = j.Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "agent.status_change",
		Severity:    "info",
		ActorType:   "system",
		Summary:     fmt.Sprintf("agent %s: online", req.AgentSlug),
		Payload:     map[string]any{"status": "online"},
	})

	if running {
		o.updateRunStatus(ctx, runState.ID, "running")
		return nil
	}

	status := "completed"
	if exitCode != 0 {
		status = "error"
		o.logger.Warn("agent exited with error", "agent_id", req.AgentID, "exit_code", exitCode)
	}
	o.updateRunStatus(ctx, runState.ID, status)

	return nil
}

// StopAccepting prevents new agent runs from starting (used during shutdown).
func (o *Orchestrator) StopAccepting() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

// RecoverFromCrash inspects all persisted run states and marks stale runs
// as completed or errored, cleaning up after an unexpected server restart.
func (o *Orchestrator) RecoverFromCrash(ctx context.Context) error {
	runs, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	for key, data := range runs {
		var run RunState
		if err := json.Unmarshal(data, &run); err != nil {
			o.logger.Warn("corrupt run state", "key", key, "error", err)
			continue
		}
		if run.Status != "running" {
			continue
		}

		if run.ExecID == "" {
			o.updateRunStatus(ctx, run.ID, "error")
			continue
		}

		running, _, err := o.container.ExecInspect(ctx, run.ExecID)
		if err != nil || !running {
			o.updateRunStatus(ctx, run.ID, "completed")
			o.logger.Info("recovered stale run", "run_id", run.ID, "agent_id", run.AgentID)
		}
	}
	return nil
}

// wrapScrubHandler returns a handler that scrubs credential patterns from
// event content before forwarding to the real handler.
// When a credential pattern is detected and redacted, a system event is emitted
// so the user can see that the scrubber is active and protecting their secrets.
func (o *Orchestrator) wrapScrubHandler(handler EventHandler) EventHandler {
	if handler == nil || o.scrubber == nil {
		return handler
	}
	var scrubNotified bool
	return func(event AgentEvent) {
		original := event.Content
		event.Content = o.scrubber.Scrub(event.Content)
		if !scrubNotified && event.Content != original {
			scrubNotified = true
			handler(AgentEvent{
				Type:      "system",
				Content:   "[security] Credential pattern detected in agent output -- redacted by stdout scrubber",
				Timestamp: time.Now(),
			})
			o.logger.Warn("scrubber redacted credential in agent output")
		}
		handler(event)
	}
}

func (o *Orchestrator) selectCredential(creds []Credential) *Credential {
	if len(creds) == 0 {
		return nil
	}
	for i := range creds {
		if !o.cooldown.IsInCooldown(creds[i].ID) {
			return &creds[i]
		}
	}
	return &creds[0]
}

// Start runs the container TTL manager, periodically stopping idle containers
// that have exceeded their configured time-to-live.
func (o *Orchestrator) Start(ctx context.Context) error {
	o.logger.Info("starting orchestrator container TTL manager")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.checkTTLs(ctx)
		}
	}
}

func (o *Orchestrator) refreshActivity(crewID, containerID string, ttlHours int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cs := o.crews[crewID]
	if cs == nil {
		cs = &crewState{}
		o.crews[crewID] = cs
	}
	cs.lastActivity = time.Now()
	cs.containerID = containerID
	if ttlHours > 0 {
		cs.ttl = time.Duration(ttlHours) * time.Hour
	} else {
		cs.ttl = 0
	}
}

func (o *Orchestrator) checkTTLs(ctx context.Context) {
	o.mu.Lock()
	var toStop []struct {
		crewID      string
		containerID string
	}
	now := time.Now()
	for crewID, cs := range o.crews {
		if cs.ttl <= 0 {
			continue
		}
		if now.Sub(cs.lastActivity) > cs.ttl {
			toStop = append(toStop, struct {
				crewID      string
				containerID string
			}{crewID: crewID, containerID: cs.containerID})
			delete(o.crews, crewID)
		}
	}
	o.mu.Unlock()

	for _, stop := range toStop {
		if stop.containerID == "" {
			continue
		}
		o.logger.Info("stopping idle crew container (TTL expired)", "crew_id", stop.crewID, "container_id", stop.containerID)
		if err := o.container.StopCrewRuntime(ctx, stop.containerID); err != nil {
			o.logger.Error("failed to stop idle crew container", "crew_id", stop.crewID, "error", err)
		}
	}
}

func (o *Orchestrator) updateRunStatus(ctx context.Context, runID, status string) {
	data, err := o.state.Get(ctx, "agent_runs", runID)
	if err != nil {
		o.logger.Error("updateRunStatus: get failed", "run_id", runID, "error", err)
		return
	}
	if data == nil {
		o.logger.Warn("updateRunStatus: run not found", "run_id", runID)
		return
	}
	var run RunState
	if err := json.Unmarshal(data, &run); err != nil {
		o.logger.Error("updateRunStatus: unmarshal failed", "run_id", runID, "error", err)
		return
	}
	run.Status = status
	run.LastActivity = time.Now()
	updated, err := json.Marshal(run)
	if err != nil {
		o.logger.Error("updateRunStatus: marshal failed", "run_id", runID, "error", err)
		return
	}
	if err := o.state.Set(ctx, "agent_runs", runID, updated); err != nil {
		o.logger.Error("updateRunStatus: set failed", "run_id", runID, "error", err)
	}
}

// buildConversationContext reads messages from the session JSONL and formats them
// as a conversation transcript for the system prompt. Uses a token budget to
// dynamically size the window — short exchanges get more turns, long tool-heavy
// turns get fewer but always include the most recent messages.
func (o *Orchestrator) buildConversationContext(ctx context.Context, sessionID string, tokenBudget int) string {
	messages, err := o.convStore.Read(ctx, sessionID, 0, 0)
	if err != nil || len(messages) == 0 {
		return ""
	}

	// Skip the current user message (just appended by bridge before RunAgent call).
	if len(messages) > 0 && messages[len(messages)-1].Role == conversation.RoleUser {
		messages = messages[:len(messages)-1]
	}
	if len(messages) == 0 {
		return ""
	}

	charBudget := tokenutil.CharsForTokens(tokenBudget)

	// Iterate backward from newest, accumulate until budget exhausted.
	var selected []conversation.Message
	totalChars := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		msgLen := len(msg.Content) + len(msg.ToolSummary)
		if totalChars+msgLen > charBudget {
			// Try to fit a truncated version of this message.
			remaining := charBudget - totalChars
			if remaining > 200 {
				truncated := msg
				if len(truncated.Content) > remaining {
					truncated.Content = truncated.Content[:remaining-20] + "...(truncated)"
					truncated.ToolSummary = ""
				}
				selected = append(selected, truncated)
			}
			break
		}
		selected = append(selected, msg)
		totalChars += msgLen
	}
	if len(selected) == 0 {
		return ""
	}

	// Reverse to chronological order.
	for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
		selected[i], selected[j] = selected[j], selected[i]
	}

	var b strings.Builder
	// Pre-size: header + per-message overhead + already-counted totalChars + trailer.
	// Avoids the Builder's geometric-growth reallocations over the loop.
	b.Grow(totalChars + len(selected)*16 + 256)

	b.WriteString("[CONVERSATION HISTORY - previous messages in this session]\n")
	for _, msg := range selected {
		// fmt.Fprintf streams directly into the Builder — the previous
		// b.WriteString(fmt.Sprintf(...)) allocated an intermediate string
		// per line that the Builder then copied into the same buffer.
		fmt.Fprintf(&b, "[%s]: %s\n", msg.Role, msg.Content)
		if msg.ToolSummary != "" {
			fmt.Fprintf(&b, "  %s\n", msg.ToolSummary)
		}
	}
	b.WriteString("[END CONVERSATION HISTORY]\n")
	b.WriteString("The user's new message follows. Continue the conversation naturally, referencing previous context when relevant.")
	return b.String()
}

// mcpPackageDomains maps well-known MCP npm packages to the API domains
// they need to reach. Used to auto-populate the sidecar allowlist in
// restricted network mode so stdio MCP servers can make outbound API calls.
var mcpPackageDomains = map[string][]string{
	"@modelcontextprotocol/server-github": {"api.github.com"},
	"@anthropic-ai/brave-search-mcp":      {"api.search.brave.com"},
	"@supabase/mcp-server-supabase":       {"api.supabase.com"},
	"@notionhq/notion-mcp-server":         {"api.notion.com"},
	"@stripe/mcp":                         {"api.stripe.com"},
	"@datadog/mcp-server":                 {"api.datadoghq.com"},
	"linear-mcp":                          {"api.linear.app"},
	"@anthropic-ai/slack-mcp":             {"slack.com"},
	"@dguido/google-workspace-mcp":        {"www.googleapis.com", "accounts.google.com", "oauth2.googleapis.com"},
	"mcp-server-sentry":                   {"sentry.io"},
}

// mcpStdioDomains extracts API domains for stdio MCP servers by matching
// their args against known packages.
// knownPackageLaunchers are commands that take a package name as the next
// non-flag argument. We only extract domains from these positions to prevent
// arbitrary args from widening the restricted-mode allowlist.
var knownPackageLaunchers = map[string]bool{
	"npx": true, "pnpm": true, "yarn": true, "bunx": true,
}

func mcpStdioDomains(servers []MCPServerConfig) []string {
	seen := make(map[string]bool)
	for _, s := range servers {
		if s.Transport != "stdio" || !knownPackageLaunchers[s.Command] {
			continue
		}
		// Find the first non-flag arg — that's the package name.
		for _, arg := range s.Args {
			if strings.HasPrefix(arg, "-") {
				continue // skip flags like -y, --quiet, dlx
			}
			pkg := normalizeNPMPackage(arg)
			if domains, ok := mcpPackageDomains[pkg]; ok {
				for _, d := range domains {
					seen[d] = true
				}
			}
			break // only the first non-flag arg is the package
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// npmSpecRe strips @version suffix from scoped and unscoped npm packages.
// "@scope/pkg@1.0.0" → "@scope/pkg", "pkg@latest" → "pkg", "-y" → "-y"
var npmSpecRe = regexp.MustCompile(`^(@[^/]+/[^@]+|[^@]+)(?:@.+)?$`)

func normalizeNPMPackage(arg string) string {
	m := npmSpecRe.FindStringSubmatch(arg)
	if len(m) > 1 {
		return m[1]
	}
	return arg
}

// TmuxSessionName returns the tmux session name for a given agent slug.
func TmuxSessionName(agentSlug string) string {
	return "agent-" + agentSlug
}

// tmuxCacheLookup returns the cached tmux-present value for containerID and
// whether the cache held an entry.
func (o *Orchestrator) tmuxCacheLookup(containerID string) (bool, bool) {
	o.tmuxCacheMu.RLock()
	defer o.tmuxCacheMu.RUnlock()
	v, ok := o.tmuxCache[containerID]
	return v, ok
}

// tmuxCacheStore records whether containerID has tmux installed. A size cap
// (tmuxCacheMaxEntries) prevents unbounded growth on long-running crewshipd
// processes that churn containers (recreate on config change, TTL cycle,
// etc.). On overflow the entire cache is flushed — cheaper than tracking
// liveness against provider state, and the worst case is a one-time re-
// probe of `command -v tmux` for each active crew (~50 ms per crew).
func (o *Orchestrator) tmuxCacheStore(containerID string, has bool) {
	o.tmuxCacheMu.Lock()
	defer o.tmuxCacheMu.Unlock()
	if len(o.tmuxCache) >= tmuxCacheMaxEntries {
		// Reset rather than evict-oldest: we do not track access time and
		// bulk clear costs nothing in Go.
		o.tmuxCache = make(map[string]bool, tmuxCacheMaxEntries)
	}
	o.tmuxCache[containerID] = has
}

// tmuxCacheMaxEntries caps the number of remembered container IDs. A busy
// workspace rarely exceeds a few dozen live containers; this cap is a safety
// net against container-ID churn leaking into long-running server memory.
const tmuxCacheMaxEntries = 1024

// InvalidateTmuxCache removes a container's cached tmux-presence entry. Called
// when a container is removed so the map does not grow unbounded across the
// lifetime of the crewshipd process (container IDs are 64 hex chars each and
// a busy workspace churns them). Safe to call for unknown IDs.
func (o *Orchestrator) InvalidateTmuxCache(containerID string) {
	o.tmuxCacheMu.Lock()
	defer o.tmuxCacheMu.Unlock()
	delete(o.tmuxCache, containerID)
}

// setupTmuxExec prepares a tmux-wrapped execution environment for an agent.
// It writes command args, env vars, and a script to files in the container
// (avoiding shell quoting issues), then returns a wrapper command that starts
// tmux and streams output via FIFO. Falls back gracefully if setup fails.
func (o *Orchestrator) setupTmuxExec(ctx context.Context, containerID string, cmd []string, agentSlug string, env []string) ([]string, error) {
	// Pre-check: fail fast if tmux is not installed in the container. Custom
	// base images (debian:bookworm-slim, ubuntu:24.04) don't ship with tmux.
	// Without this check, the outer wrapper runs anyway and produces noisy
	// stderr output before falling back, which confuses users.
	//
	// Result is cached per container — tmux presence is fixed once the image
	// is built, so repeating the probe on every run (every agent message) was
	// a 50 ms tax for no information. Cache is invalidated naturally when the
	// container is recreated with a new ID.
	if has, ok := o.tmuxCacheLookup(containerID); ok {
		if !has {
			return nil, fmt.Errorf("tmux not installed in container")
		}
	} else {
		checkResult, checkErr := o.container.Exec(ctx, provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         []string{"sh", "-c", "command -v tmux >/dev/null 2>&1"},
			User:        "1001:1001",
		})
		if checkErr != nil {
			return nil, fmt.Errorf("tmux check: %w", checkErr)
		}
		io.Copy(io.Discard, checkResult.Reader)
		checkResult.Reader.Close()
		_, tmuxExitCode, inspectErr := o.container.ExecInspect(ctx, checkResult.ExecID)
		if inspectErr != nil {
			return nil, fmt.Errorf("tmux check inspect: %w", inspectErr)
		}
		has := tmuxExitCode == 0
		o.tmuxCacheStore(containerID, has)
		if !has {
			return nil, fmt.Errorf("tmux not installed in container")
		}
	}

	session := TmuxSessionName(agentSlug)
	argsFile := fmt.Sprintf("/tmp/%s.args", session)
	scriptFile := fmt.Sprintf("/tmp/%s.sh", session)
	fifo := fmt.Sprintf("/tmp/%s.fifo", session)
	exitFile := fmt.Sprintf("/tmp/%s.exit", session)
	doneSignal := session + "-done"
	envFile := fmt.Sprintf("/tmp/%s.env", session)

	// Step 1: Write null-separated command args to file via base64.
	var argsBuf []byte
	for _, arg := range cmd {
		argsBuf = append(argsBuf, []byte(arg)...)
		argsBuf = append(argsBuf, 0)
	}
	argsEncoded := base64.StdEncoding.EncodeToString(argsBuf)
	writeArgsResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", argsEncoded, argsFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write args file: %w", err)
	}
	io.Copy(io.Discard, writeArgsResult.Reader)
	writeArgsResult.Reader.Close()

	// Step 2: Write env vars as sourceable shell script.
	var envScript strings.Builder
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			key := e[:idx]
			// Only allow safe env var names ([A-Za-z_][A-Za-z0-9_]*) to prevent
			// shell injection via crafted key names in the sourced export script.
			safe := true
			for i, c := range key {
				if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' || (i > 0 && c >= '0' && c <= '9')) {
					safe = false
					break
				}
			}
			if !safe || len(key) == 0 {
				continue
			}
			val := e[idx+1:]
			escaped := strings.ReplaceAll(val, "'", "'\\''")
			envScript.WriteString(fmt.Sprintf("export %s='%s'\n", key, escaped))
		}
	}
	envEncoded := base64.StdEncoding.EncodeToString([]byte(envScript.String()))
	envWriteResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s'", envEncoded, envFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write env file: %w", err)
	}
	io.Copy(io.Discard, envWriteResult.Reader)
	envWriteResult.Reader.Close()

	// Step 3: Write inner script (sources env, runs command via xargs).
	scriptContent := fmt.Sprintf("#!/bin/sh\n. '%s'\n"+
		"EX=0\nxargs -0 stdbuf -oL < '%s' > '%s' 2>&1 || EX=$?\necho $EX > '%s'\nrm -f '%s'\ntmux wait-for -S '%s'\n",
		envFile, argsFile, fifo, exitFile, fifo, doneSignal)
	scriptEncoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))
	writeScriptResult, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", fmt.Sprintf("printf '%%s' '%s' | base64 -d > '%s' && chmod +x '%s'", scriptEncoded, scriptFile, scriptFile)},
		User:        "1001:1001",
	})
	if err != nil {
		return nil, fmt.Errorf("write script file: %w", err)
	}
	io.Copy(io.Discard, writeScriptResult.Reader)
	writeScriptResult.Reader.Close()

	// Step 4: Return outer wrapper. Uses session-scoped kill (not kill-server)
	// to avoid disrupting other agent sessions in the same crew container.
	// If tmux new-session fails, falls back to direct exec via sh.
	wrapper := fmt.Sprintf(
		"tmux kill-session -t '%s' 2>/dev/null; rm -f '%s' '%s'; mkfifo '%s'; "+
			"if tmux new-session -d -s '%s' -x 200 -y 50 'sh %s'; then "+
			"cat '%s' 2>/dev/null; "+
			"tmux wait-for '%s' 2>/dev/null || true; "+
			"else sh '%s'; fi; "+
			"EC=0; [ -f '%s' ] && EC=$(cat '%s') && rm -f '%s'; "+
			"rm -f '%s' '%s' '%s'; exit $EC",
		session, fifo, exitFile, fifo,
		session, scriptFile,
		fifo,
		doneSignal,
		scriptFile,
		exitFile, exitFile, exitFile,
		scriptFile, argsFile, envFile,
	)
	return []string{"sh", "-c", wrapper}, nil
}
