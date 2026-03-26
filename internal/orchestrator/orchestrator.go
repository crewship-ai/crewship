package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/scrubber"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

type AgentRunRequest struct {
	AgentID         string
	AgentSlug       string
	AgentRole       string // AGENT, LEAD, COORDINATOR
	CrewID          string
	CrewSlug        string
	ChatID          string
	WorkspaceID     string
	ContainerID     string
	CLIAdapter      string // CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI
	LLMModel        string // optional model override (e.g. claude-haiku-4-5-20251001)
	SystemPrompt    string
	UserMessage     string
	ToolProfile     string // MINIMAL, CODING, MESSAGING, FULL
	Credentials     []Credential
	TimeoutSecs     int
	MemoryEnabled   bool
	CrewMembers     []CrewMember // Populated by bridge for LEAD agents
	AllCrews        []CrewInfo        // Populated by bridge for COORDINATOR agents
	ActiveMissions  []MissionSummary  // Active missions in workspace (for COORDINATOR)
	SkipSidecar     bool         // When true, skip sidecar even if enabled globally (prevents port conflict in sub-agents)
	SkipConvHistory bool         // When true, skip injecting conversation history (used by assignment sub-agents)
	NetworkMode     string       // "free" (default) or "restricted" — crew-level network policy
	AllowedDomains  []string     // Extra allowed domains for restricted mode
	MemoryMB        int
	CPUs            float64
	TTLHours        int
	MCPServers      []MCPServerConfig // Resolved MCP server configs for this agent
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
	Type       string `json:"type"`   // "bearer", "api_key", "basic"
	Header     string `json:"header,omitempty"`
}

type Credential struct {
	ID         string `json:"id,omitempty"`
	EnvVarName string `json:"env_var"`
	PlainValue string `json:"value"`
	Priority   int    `json:"priority"`
	Type       string `json:"type,omitempty"` // API_KEY, AI_CLI_TOKEN, SECRET
}

type RunState struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	ChatID    string    `json:"chat_id"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	ContainerID  string    `json:"container_id"`
	ExecID       string    `json:"exec_id"`
	LastActivity time.Time `json:"last_activity"`
	CredentialID string    `json:"credential_id,omitempty"`
}

type AgentEvent struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Metadata  any       `json:"metadata,omitempty"`
	Timestamp time.Time `json:"ts"`
}

type EventHandler func(event AgentEvent)

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
	mu             sync.Mutex
	accepting      bool
	crewLastActivity map[string]time.Time
	crewTTL          map[string]time.Duration
	crewContainers   map[string]string
}

func New(
	container provider.ContainerProvider,
	state provider.StateProvider,
	logger *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		container:        container,
		state:            state,
		scrubber:         scrubber.New(),
		logger:           logger,
		cooldown:         NewCooldownManager(),
		accepting:        true,
		crewLastActivity: make(map[string]time.Time),
		crewTTL:          make(map[string]time.Duration),
		crewContainers:   make(map[string]string),
	}
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

func (o *Orchestrator) SetKeeperEnabled(enabled bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.keeperEnabled = enabled
}

func (o *Orchestrator) KeeperEnabled() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
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
func (o *Orchestrator) GetOrCreateContainer(ctx context.Context, crewSlug, crewID string) (string, error) {
	if o.container == nil {
		return "", fmt.Errorf("container provider not configured")
	}
	return o.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:   crewID,
		Slug: crewSlug,
	})
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

func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	o.mu.Lock()
	if !o.accepting {
		o.mu.Unlock()
		return fmt.Errorf("orchestrator not accepting new runs")
	}
	o.mu.Unlock()

	if req.ContainerID != "" {
		o.refreshActivity(req.CrewID, req.ContainerID, req.TTLHours)
		defer o.refreshActivity(req.CrewID, req.ContainerID, req.TTLHours)
	}

	runState := RunState{
		ID:          req.ChatID,
		AgentID:     req.AgentID,
		ChatID:   req.ChatID,
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

	// Inject lead crew context into system prompt (before memory, after conversation history)
	if req.AgentRole == "LEAD" && len(req.CrewMembers) > 0 {
		leadCtx := BuildLeadContext(req.CrewMembers)
		if leadCtx != "" {
			req.SystemPrompt = req.SystemPrompt + "\n\n" + leadCtx
		}
	}

	// Inject coordinator context listing all workspace crews
	if req.AgentRole == "COORDINATOR" && len(req.AllCrews) > 0 {
		coordCtx := BuildCoordinatorContext(req.AllCrews, req.ActiveMissions)
		if coordCtx != "" {
			req.SystemPrompt = req.SystemPrompt + "\n\n" + coordCtx
		}
	}

	// Inject peer communication context for non-LEAD agents in a crew
	if req.AgentRole != "LEAD" && len(req.CrewMembers) > 0 {
		peerCtx := BuildPeerContext(req.CrewMembers, req.AgentSlug)
		if peerCtx != "" {
			req.SystemPrompt = req.SystemPrompt + "\n\n" + peerCtx
		}
	}

	// Inject agent memory context into system prompt (after conversation history)
	if req.MemoryEnabled {
		memoryCtx := o.buildMemoryContext(ctx, req, tokenutil.CharsForTokens(memTokenBudget))
		if memoryCtx != "" {
			req.SystemPrompt = req.SystemPrompt + "\n\n" + memoryCtx
		}
	}

	o.logger.Info("system prompt assembled",
		"agent", req.AgentSlug,
		"est_tokens", tokenutil.EstimateTokens(req.SystemPrompt),
	)

	o.mu.Lock()
	sidecarEnabled := o.sidecarEnabled && !req.SkipSidecar
	keeperEnabled := o.keeperEnabled
	ipcBaseURL := o.ipcBaseURL
	ipcToken := o.ipcToken
	o.mu.Unlock()

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
			}
		}
		// Build IPC config for agents in a crew so the sidecar can forward
		// assignment requests (LEAD), peer queries, and escalations (all roles)
		var ipcCfg *SidecarIPCConfig
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
			networkPolicy = &SidecarNetworkPolicy{
				Mode:           "restricted",
				AllowedDomains: req.AllowedDomains,
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
			if health.NetworkMode == desiredMode {
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

	// Write MCP server configuration file (if integrations are assigned)
	if err := setupMCPConfig(ctx, o.container, req.ContainerID, req.AgentSlug, req.MCPServers, o.logger); err != nil {
		o.logger.Warn("failed to inject MCP config", "error", err, "agent_id", req.AgentID)
	}

	// Write CLI-specific system prompt files (e.g. AGENTS.md for OpenCode)
	if err := setupSystemPromptFiles(ctx, o.container, req.ContainerID, req, workDir, o.logger); err != nil {
		o.logger.Warn("failed to write system prompt files", "error", err, "agent_id", req.AgentID, "cli_adapter", req.CLIAdapter)
	}

	// Wrap agent CLI command with stdbuf to force line-buffered stdout.
	// Apple's container runtime buffers exec output which causes choppy
	// streaming in chat. stdbuf -oL flushes on every newline so JSON
	// stream events arrive immediately.
	execCmd := append([]string{"stdbuf", "-oL"}, cmd...)

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

	result, err := o.container.Exec(execCtx, execCfg)
	if err != nil {
		o.logger.Error("exec agent failed", "error", err, "agent_id", req.AgentID)
		o.updateRunStatus(ctx, runState.ID, "error")
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

func (o *Orchestrator) StopAccepting() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

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
	o.crewLastActivity[crewID] = time.Now()
	o.crewContainers[crewID] = containerID
	if ttlHours > 0 {
		o.crewTTL[crewID] = time.Duration(ttlHours) * time.Hour
	} else {
		delete(o.crewTTL, crewID)
	}
}

func (o *Orchestrator) checkTTLs(ctx context.Context) {
	o.mu.Lock()
	var toStop []struct {
		crewID      string
		containerID string
	}
	now := time.Now()
	for crewID, last := range o.crewLastActivity {
		ttl, ok := o.crewTTL[crewID]
		if !ok || ttl <= 0 {
			continue
		}
		if now.Sub(last) > ttl {
			toStop = append(toStop, struct {
				crewID      string
				containerID string
			}{crewID: crewID, containerID: o.crewContainers[crewID]})
			delete(o.crewLastActivity, crewID)
			delete(o.crewContainers, crewID)
			delete(o.crewTTL, crewID)
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
	b.WriteString("[CONVERSATION HISTORY - previous messages in this session]\n")
	for _, msg := range selected {
		b.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
		if msg.ToolSummary != "" {
			b.WriteString(fmt.Sprintf("  %s\n", msg.ToolSummary))
		}
	}
	b.WriteString("[END CONVERSATION HISTORY]\n")
	b.WriteString("The user's new message follows. Continue the conversation naturally, referencing previous context when relevant.")
	return b.String()
}
