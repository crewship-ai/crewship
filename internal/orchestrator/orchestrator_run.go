package orchestrator

// Run-time execution paths extracted from orchestrator.go for readability.
// All public function signatures are unchanged; this is a pure file move.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

func (o *Orchestrator) RunAgentForAssignment(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	req.SkipConvHistory = true
	return o.RunAgent(ctx, req, handler)
}

// SetConversationStore sets the conversation store for reading session history.
func (o *Orchestrator) SetConversationStore(store *conversation.Store) {
	o.convStore = store
}

// RunAgent executes an agent run inside its crew's container, streaming events

func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	o.mu.RLock()
	if !o.accepting {
		o.mu.RUnlock()
		return fmt.Errorf("orchestrator not accepting new runs")
	}
	o.mu.RUnlock()

	// Capture the user prompt that triggered this run as a journal
	// entry. Lands in the Timeline as the "what kicked this off"
	// signal — without it, a viewer scrolling through exec.command +
	// network.egress can't reconstruct WHY the agent was doing those
	// things. Best-effort: a journal hiccup never aborts the run.
	if req.UserMessage != "" {
		userPreview := req.UserMessage
		if len(userPreview) > 240 {
			userPreview = userPreview[:240] + "…"
		}
		_, _ = o.getJournal().Emit(ctx, JournalEntry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Type:        "chat.user_message",
			Severity:    "info",
			ActorType:   "user",
			ActorID:     req.AgentID, // best available; agents-context call sites rarely carry user_id
			Summary:     fmt.Sprintf("user → %s: %s", req.AgentSlug, userPreview),
			Payload: map[string]any{
				"chat_id":      req.ChatID,
				"agent_slug":   req.AgentSlug,
				"content":      req.UserMessage,
				"length_chars": len(req.UserMessage),
			},
			Refs: map[string]any{"chat_id": req.ChatID},
		})
	}

	// Harbor Master: gate the run before we spend containers/tokens on
	// something a human should approve. ApprovalMode comes off the
	// request — ModeNone short-circuits with Approved, ModeSync blocks
	// here until a human decides, ModeAsync enqueues and returns
	// Pending. Rules hit only fire when ApprovalMode != "none".
	approvalMode := req.ApprovalMode
	if approvalMode == "" {
		approvalMode = "none"
	}
	gateDecision, gateErr := o.getApprovalGate().Check(ctx, ApprovalCheckInput{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Tool:        "agent_run",
		Args: map[string]any{
			"agent_slug":  req.AgentSlug,
			"agent_role":  req.AgentRole,
			"user_prompt": truncateStr(req.UserMessage, 500),
		},
		Mode:   approvalMode,
		UserID: req.AgentID,
	})
	if gateErr != nil {
		return fmt.Errorf("approval gate: %w", gateErr)
	}
	// If the gate enqueued an approval (Required && !Approved → Pending,
	// or Denied with an existing request row), fire the
	// on_approval_requested hook so integrations can notify a human
	// (Slack, PagerDuty, etc.) without waiting for the journal poller.
	// Fire on any gated decision — denied and pending both mean "a
	// human needs to know about this", approved means the rule matched
	// and a prior approval auto-satisfied it.
	if gateDecision.Required {
		_ = o.getHooks().Dispatch(ctx, "on_approval_requested", HookEventContext{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			ToolName:    "agent_run",
			Severity:    "notice",
			Payload: map[string]any{
				"request_id": gateDecision.RequestID,
				"reason":     gateDecision.Reason,
				"approved":   gateDecision.Approved,
				"denied":     gateDecision.Denied,
				"pending":    gateDecision.Pending,
				"agent_slug": req.AgentSlug,
			},
		})
	}
	// Only proceed when the gate explicitly says so. Denied, Pending,
	// and "Required but not Approved" must all halt the run — a
	// pending approval still means a human hasn't said yes yet, and
	// falling through in that state would defeat the point of HITL.
	if gateDecision.Denied {
		return fmt.Errorf("run denied by approval: %s", gateDecision.Reason)
	}
	if gateDecision.Required && !gateDecision.Approved {
		state := "pending"
		if gateDecision.Pending {
			state = "pending"
		}
		return fmt.Errorf("run requires approval (%s): request_id=%s reason=%s",
			state, gateDecision.RequestID, gateDecision.Reason)
	}

	// Hooks: fire pre_agent_start. Blocking hooks can abort the run;
	// non-blocking hooks run in goroutines and don't affect latency.
	// The dispatcher returns *hooks.BlockedError for blocking-hook
	// refusal; we surface that to the caller as-is so the UI can
	// render the block reason.
	if hookErr := o.getHooks().Dispatch(ctx, "pre_agent_start", HookEventContext{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		ToolName:    "agent_run",
		Severity:    "info",
		Payload: map[string]any{
			"agent_slug": req.AgentSlug,
			"chat_id":    req.ChatID,
		},
	}); hookErr != nil {
		return fmt.Errorf("pre_agent_start hook blocked: %w", hookErr)
	}
	// Always fire post_agent_stop on return so logging and cleanup
	// hooks observe every run regardless of exit path.
	defer func() {
		_ = o.getHooks().Dispatch(context.Background(), "post_agent_stop", HookEventContext{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			ToolName:    "agent_run",
			Payload:     map[string]any{"agent_slug": req.AgentSlug, "chat_id": req.ChatID},
		})
	}()

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

	// Inject peer communication context for non-LEAD agents in a crew
	if req.AgentRole != "LEAD" && len(req.CrewMembers) > 0 {
		if peerCtx := BuildPeerContext(req.CrewMembers, req.AgentSlug); peerCtx != "" {
			promptBuf.WriteString("\n\n")
			promptBuf.WriteString(peerCtx)
		}
	}

	// Episodic recall: ask the memory layer for past high-value events
	// similar to the current user prompt. Regular agents see only their
	// own history; LEAD sees crew-shared entries too (the scope rule is
	// inside the recaller). The injection is best-effort
	// — a recall failure logs and continues so a flaky Ollama embed
	// service never blocks a run. Budget is 2 KB of the 8 KB headroom
	// reserved in promptBuf.Grow above.
	if recaller := o.getEpisodicRecall(); recaller != nil && req.UserMessage != "" {
		recallCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		rendered, err := recaller.Recall(recallCtx, EpisodicRecallInput{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			Role:        req.AgentRole,
			Query:       req.UserMessage,
			MaxChars:    2000,
		})
		cancel()
		if err != nil {
			// "Ollama unreachable" is the common dev-mode case (no embedder
			// running). It's expected, not a bug, but with N parallel agent
			// runs the per-recall DEBUG line floods the log on every chat
			// turn. Log at INFO at most once per episodicUnreachableLogInterval
			// so a single outage doesn't spam, but a *new* outage after
			// recovery still surfaces.
			if strings.Contains(err.Error(), "ollama unreachable") {
				now := time.Now().UnixNano()
				last := o.episodicUnreachableLastLogged.Load()
				if last == 0 || time.Duration(now-last) >= episodicUnreachableLogInterval {
					if o.episodicUnreachableLastLogged.CompareAndSwap(last, now) {
						o.logger.Info("episodic recall backend unreachable; continuing without recall", "err", err)
					}
				}
			} else {
				o.logger.Debug("episodic recall failed; continuing without", "err", err, "agent", req.AgentSlug)
			}
		} else {
			// Any successful recall (even an empty result for queries with
			// no matches) means the backend is healthy again; reset the
			// dedup so a *future* outage logs anew.
			o.episodicUnreachableLastLogged.Store(0)
			if rendered != "" {
				promptBuf.WriteString("\n\n")
				promptBuf.WriteString(rendered)
			}
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
		if ipcBaseURL != "" && (req.AgentRole == "LEAD" || len(req.CrewMembers) > 0) {
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

	// Write MCP server configuration via the per-CLI adapter. Each adapter
	// knows its own file path + format (Claude .mcp.json, Codex
	// .codex/config.toml, Gemini .gemini/settings.json, OpenCode opencode.json
	// under "mcp" key, Cursor .cursor/mcp.json, Droid .factory/mcp.json).
	// Adapters that don't support MCP (currently none after the multi-CLI
	// wave; unknownAdapter only) make this a no-op.
	mcpAdapter := getAdapter(req.CLIAdapter)
	if mcpAdapter.SupportsMCP() {
		if err := mcpAdapter.WriteMCPConfig(ctx, o.container, req.ContainerID, req, workDir, o.logger); err != nil {
			hasMCP := req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" || len(req.MCPServers) > 0
			if hasMCP {
				o.updateRunStatus(ctx, runState.ID, "error")
				return fmt.Errorf("inject MCP config (%s): %w", req.CLIAdapter, err)
			}
			o.logger.Warn("failed to inject MCP config", "error", err, "agent_id", req.AgentID, "cli_adapter", req.CLIAdapter)
		}
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
		MissionID:   req.MissionID,
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
	// Flip the Watch Roster to busy AND emit the agent.status_change
	// journal entry on transition. Tracker implementation
	// (server/presence_adapter.go) owns both writes so the row and
	// the journal stay in lock-step — previously only the journal
	// entry fired and /crows-nest always showed an empty roster.
	_ = o.getPresence().Track(ctx, PresenceInput{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		Status:      "busy",
		Details:     map[string]any{"current_chat_id": req.ChatID},
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
			MissionID:   req.MissionID,
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
	// Tap text events flowing to the user so we can emit one
	// chat.agent_response journal entry at end-of-run with the
	// agent's full reply. Capped buffer (8 KB) keeps memory bounded
	// for chatty replies while still preserving the typical "thinking
	// + final answer" payload size.
	const responseCap = 8 * 1024
	var responseBuf strings.Builder
	tappedHandler := EventHandler(func(event AgentEvent) {
		if event.Type == "text" && responseBuf.Len() < responseCap {
			remaining := responseCap - responseBuf.Len()
			if len(event.Content) <= remaining {
				responseBuf.WriteString(event.Content)
			} else {
				responseBuf.WriteString(event.Content[:remaining])
			}
		}
		if scrubHandler != nil {
			scrubHandler(event)
		}
	})
	o.streamOutput(execCtx, result, req, tappedHandler)
	if response := strings.TrimSpace(responseBuf.String()); response != "" {
		responseSummary := response
		if len(responseSummary) > 240 {
			responseSummary = responseSummary[:240] + "…"
		}
		// Emit fires regardless of cancellation — context.Background
		// guarantees the entry lands even on a stop. The truncation
		// happens at the buffer level upstream, so length_chars is the
		// true reply size while `content` is the captured prefix.
		emitCtx := ctx
		if emitCtx.Err() != nil {
			emitCtx = context.Background()
		}
		_, _ = o.getJournal().Emit(emitCtx, JournalEntry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Type:        "chat.agent_response",
			Severity:    "info",
			ActorType:   "agent",
			ActorID:     req.AgentID,
			Summary:     fmt.Sprintf("%s → user: %s", req.AgentSlug, responseSummary),
			Payload: map[string]any{
				"chat_id":      req.ChatID,
				"agent_slug":   req.AgentSlug,
				"content":      response,
				"length_chars": responseBuf.Len(),
				"truncated":    responseBuf.Len() >= responseCap,
			},
			Refs: map[string]any{"chat_id": req.ChatID},
		})
	}

	// If context was cancelled (user pressed stop), clean up with a fresh
	// context and return a cancellation error. The reader close in streamOutput
	// sends SIGPIPE to the exec process, which should terminate it.
	if ctx.Err() != nil {
		cleanCtx := context.Background()
		o.updateRunStatus(cleanCtx, runState.ID, "cancelled")
		o.logger.Info("run cancelled", "agent_id", req.AgentID, "exec_id", result.ExecID)
		// Close the Crow's Nest exec.command block and flip the Watch
		// Roster off busy on cancellation too — otherwise stopped
		// runs leave the live terminal hanging and presence stuck on
		// "busy" until the 5-min idle sweeper corrects it. Uses
		// cleanCtx so journal writes complete even after ctx expired.
		_, _ = j.Emit(cleanCtx, JournalEntry{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Type:        "exec.command",
			Severity:    "warn",
			ActorType:   "agent",
			ActorID:     req.AgentID,
			Summary:     fmt.Sprintf("%s: CANCELLED (%dms)", req.AgentSlug, time.Since(execStart).Milliseconds()),
			Payload: map[string]any{
				"cmd":         cmd,
				"phase":       "end",
				"cancelled":   true,
				"duration_ms": time.Since(execStart).Milliseconds(),
			},
			Refs: map[string]any{"chat_id": req.ChatID, "exec_id": result.ExecID},
		})
		_ = o.getPresence().Track(cleanCtx, PresenceInput{
			WorkspaceID: req.WorkspaceID,
			CrewID:      req.CrewID,
			AgentID:     req.AgentID,
			MissionID:   req.MissionID,
			Status:      "online",
			Details:     map[string]any{"reason": "cancelled"},
		})
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
		MissionID:   req.MissionID,
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
	_ = o.getPresence().Track(ctx, PresenceInput{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		Status:      "online",
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

	// Capture the container's actual installed-package state — apt + pip
	// + npm + os-release. The journal is the source of truth for "what
	// happened in this crew" and devcontainer.json is just declared
	// intent; agents that ran apt-get / pip install during the session
	// drift those two apart, and a quiet session writes no entry thanks
	// to hash-based dedup. Best-effort: failures are swallowed inside
	// recordContainerSnapshot so a probe error never breaks the run.
	o.recordContainerSnapshot(ctx, req, req.ContainerID)

	return nil
}

// Start runs the container TTL manager, periodically stopping idle containers
// that have exceeded their configured time-to-live.

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
