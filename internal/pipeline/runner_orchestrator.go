package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// OrchestratorRunner satisfies pipeline.AgentRunner by routing each
// step through the same orchestrator path that powers chat-driven
// agent runs. The chosen agent (resolved from author_crew + slug)
// runs in its real container, with its real CLI adapter, system
// prompt, skills, MCP servers, and credentials.
//
// This is the model Pavel called "the analogy of people in a
// company": pipelines reuse the firm's existing employees rather
// than hiring new ones via API. Crewship never holds a raw API
// key for the LLM provider — the agent's CLI tool (Claude Code,
// Codex, Gemini, etc.) does the auth via its own token. The
// pipeline runtime just hands the prompt to the agent through the
// orchestrator and captures the assistant's output.
//
// Per-step lifecycle:
//
//  1. Look up agent_id from (author_crew_id, agent_slug).
//  2. resolver.ResolveAgent → ChatInfo with full config.
//  3. EnsureCrewRuntime to spin up / reuse the crew container.
//  4. Persist a synthetic chat (so the conversation store has a
//     record and the journal can join run → chat).
//  5. Build orchestrator.AgentRunRequest, call RunAgent with a
//     buffering EventHandler that captures "text" + "result"
//     events.
//  6. On completion, return the assembled assistant text.
//
// The runner is stateless across calls — every step gets a fresh
// chat session. This keeps each step deterministic with respect
// to its own inputs (no implicit memory bleed across steps within
// one pipeline run; the executor's StepOutputs map is the only
// communication channel).
type OrchestratorRunner struct {
	db        *sql.DB
	orch      *orchestrator.Orchestrator
	container provider.ContainerProvider
	resolver  chatbridge.ChatResolver
	logWriter *logcollector.Writer
	convStore *conversation.Store
	journalE  journal.Emitter
	logger    *slog.Logger
}

// OrchestratorRunnerDeps bundles the runner's dependencies. Passed
// as one struct so the call site (cmd_start.go) doesn't need to
// remember positional argument order — every field has a
// documented purpose so a wiring miss is easy to spot at the
// construction site.
type OrchestratorRunnerDeps struct {
	DB        *sql.DB
	Orch      *orchestrator.Orchestrator
	Container provider.ContainerProvider
	Resolver  chatbridge.ChatResolver
	LogWriter *logcollector.Writer // optional
	ConvStore *conversation.Store  // optional
	Journal   journal.Emitter      // optional
	Logger    *slog.Logger
}

// NewOrchestratorRunner returns a runner wired against the supplied
// dependencies. DB, Orch, Container, and Resolver are required;
// the rest may be nil and the runner falls back to no-ops.
func NewOrchestratorRunner(deps OrchestratorRunnerDeps) (*OrchestratorRunner, error) {
	if deps.DB == nil {
		return nil, errors.New("OrchestratorRunner: DB required")
	}
	if deps.Orch == nil {
		return nil, errors.New("OrchestratorRunner: Orchestrator required")
	}
	if deps.Container == nil {
		return nil, errors.New("OrchestratorRunner: ContainerProvider required")
	}
	if deps.Resolver == nil {
		return nil, errors.New("OrchestratorRunner: ChatResolver required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &OrchestratorRunner{
		db:        deps.DB,
		orch:      deps.Orch,
		container: deps.Container,
		resolver:  deps.Resolver,
		logWriter: deps.LogWriter,
		convStore: deps.ConvStore,
		journalE:  deps.Journal,
		logger:    deps.Logger,
	}, nil
}

// RunStep is the AgentRunner contract entry point. Each call is one
// LLM-equivalent invocation against the agent identified by the
// (AuthorCrewID, AgentSlug) pair on the request. We deliberately
// shadow the executor's deadline by setting our own (via the
// AgentRunRequest TimeoutSecs) so an unresponsive agent doesn't
// hang the executor goroutine — the orchestrator enforces it.
func (r *OrchestratorRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	// 1. Resolve agent_id from (workspace_id, crew_id, agent_slug).
	// Workspace constraint is critical: the lookup must verify the
	// crew belongs to the calling workspace, otherwise an
	// AuthorCrewID pointing to a crew in another workspace would
	// silently execute that workspace's agent on this workspace's
	// data.
	agentID, err := r.resolveAgentID(ctx, req.WorkspaceID, req.AuthorCrewID, req.AgentSlug)
	if err != nil {
		return AgentStepResult{}, fmt.Errorf("resolve agent: %w", err)
	}

	// 2. ResolveAgent → ChatInfo with credentials, system prompt,
	//    skills, MCP servers etc. The resolver hits the internal
	//    /api/v1/internal/agents/{id}/resolve endpoint so we get
	//    the same configuration the chat handler uses.
	// req.WorkspaceID is passed so the resolver's server-side scope engages:
	// agentID was already workspace-validated by resolveAgentID above, and
	// sending the workspace makes the resolve query reject any id that
	// somehow points outside this workspace (defence-in-depth, 404).
	info, err := r.resolver.ResolveAgent(ctx, agentID, req.WorkspaceID)
	if err != nil {
		return AgentStepResult{}, fmt.Errorf("resolve agent config: %w", err)
	}

	// 3. EnsureCrewRuntime — spawn the container if missing, reuse
	//    if already running. This is the same primitive the chat
	//    handler uses; pipelines don't get a separate container
	//    pool, they share the crew's existing runtime.
	containerID, err := r.container.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID:          info.CrewID,
		Slug:        info.CrewSlug,
		Image:       info.RuntimeImage,
		CachedImage: info.CachedImage,
		// MemoryMB / CPUs default to the orchestrator's defaults
		// when zero — the runner doesn't override those because
		// pipelines aren't currently sized differently from chat
		// runs.
	})
	if err != nil {
		return AgentStepResult{}, fmt.Errorf("ensure container: %w", err)
	}

	// 4. Synthetic chat session. We mint a fresh chat per step so
	//    journal/audit can join: pipeline_run -> step -> chat ->
	//    agent_run. The chat title encodes the pipeline + step ID
	//    so the chats list shows "Pipeline X / step fetch" rather
	//    than a bare UUID.
	chatID := generateRunID() // reuse run-id minter; format is fine
	chatTitle := fmt.Sprintf("Pipeline %s · step %s", req.PipelineID, req.StepID)
	if err := r.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     agentID,
		WorkspaceID: req.WorkspaceID,
		Title:       chatTitle,
	}); err != nil {
		// Non-fatal: a missing chat row degrades the audit trail
		// but doesn't break the run. Log and continue.
		r.logger.Warn("pipeline orchestrator runner: create chat failed",
			"error", err, "pipeline_id", req.PipelineID, "step_id", req.StepID)
	}

	// 5. Persist the rendered prompt as the user message so the
	//    chat's conversation history shows what the pipeline asked.
	if r.convStore != nil {
		_ = r.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateRunID(),
			Role:      conversation.RoleUser,
			Content:   req.Prompt,
			Timestamp: time.Now().UTC(),
		})
	}

	// 6. Build AgentRunRequest.
	//
	// Tier resolution honoring: when the executor's tier resolver
	// produced a non-empty Model on the request, override the agent's
	// default. This is the load-bearing wire for two-tier execution
	// — without it a routine's `complexity: "fast"` would silently
	// run on whatever model the agent was created with (typically
	// Sonnet), defeating the cost reduction promise.
	//
	// CLIAdapter is intentionally NOT overridden from req.Adapter:
	// (a) the workspace tier config's "adapter" field is shorthand
	// ("claude" / "gemini") not the orchestrator's constants
	// ("CLAUDE_CODE" / "GEMINI_CLI"), so direct override produces
	// an unrecognized adapter and falls through to a bare CLI invocation
	// missing system prompt / mcp config / etc.;
	// (b) the dominant tier-swap use-case is cheap-vs-expensive on the
	// SAME provider (Haiku → Opus), where adapter stays constant and
	// only model changes;
	// (c) cross-adapter swap (Claude → Gemini) is a rare advanced
	// case worth a follow-up that maps shorthand → constant.
	//
	// SystemPrompt and ToolProfile likewise stay agent-defined — the
	// routine doesn't get to mess with persona or tool whitelist.
	cliAdapter := info.CLIAdapter
	llmModel := info.LLMModel
	if req.Model != "" {
		llmModel = req.Model
	}
	// Pick the tighter of the two timeouts (agent default vs step
	// override). When the agent has no configured timeout
	// (info.TimeoutSecs == 0), the previous form `req.TimeoutSec <
	// timeoutSecs` evaluated to `N < 0` → false and silently
	// dropped the step's requested timeout. The fix: apply the
	// step override whenever it's positive AND either the agent
	// has no default OR the step is tighter.
	timeoutSecs := info.TimeoutSecs
	if req.TimeoutSec > 0 {
		if timeoutSecs == 0 || req.TimeoutSec < timeoutSecs {
			timeoutSecs = req.TimeoutSec
		}
	}
	if timeoutSecs == 0 {
		timeoutSecs = 600 // 10-minute default; agents that need
		// longer override on a per-step basis via DSL TimeoutSec.
	}
	runReq := orchestrator.AgentRunRequest{
		AgentID:            info.AgentID,
		AgentSlug:          info.AgentSlug,
		AgentRole:          info.AgentRole,
		CrewID:             info.CrewID,
		CrewSlug:           info.CrewSlug,
		WorkspaceID:        info.WorkspaceID,
		ChatID:             chatID,
		ContainerID:        containerID,
		CLIAdapter:         cliAdapter,
		LLMModel:           llmModel,
		SystemPrompt:       info.SystemPrompt,
		UserMessage:        req.Prompt,
		ToolProfile:        info.ToolProfile,
		Credentials:        info.Credentials,
		TimeoutSecs:        timeoutSecs,
		MemoryEnabled:      info.MemoryEnabled,
		CrewMembers:        info.CrewMembers,
		NetworkMode:        info.NetworkMode,
		AllowedDomains:     info.AllowedDomains,
		MCPServers:         info.MCPServers,
		CrewMCPConfigJSON:  info.CrewMCPConfigJSON,
		AgentMCPConfigJSON: info.AgentMCPConfigJSON,
		PreferredLanguage:  info.PreferredLanguage,
		Skills:             info.InstalledSkills,
	}

	// 7. Run with buffering handler that captures text + result
	//    events. The "result" event carries usage metadata
	//    (token counts, cost) that we surface as AgentStepResult
	//    so the executor's run summary is accurate.
	startedAt := time.Now()
	var fullResponse strings.Builder
	var resultMeta map[string]any

	var logBuf *logcollector.OutputBuffer
	if r.logWriter != nil {
		logBuf = logcollector.NewOutputBuffer(r.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()
	}

	handler := func(event orchestrator.AgentEvent) {
		switch event.Type {
		case "text":
			fullResponse.WriteString(event.Content)
		case "result":
			if m, ok := event.Metadata.(map[string]any); ok {
				resultMeta = m
			}
		}
		if logBuf != nil {
			_ = logBuf.Append(logcollector.LogEntry{
				Timestamp: event.Timestamp,
				Level:     "info",
				Agent:     info.AgentSlug,
				Event:     event.Type,
				Content:   event.Content,
				Metadata:  event.Metadata,
			})
		}
	}

	if err := r.orch.RunAgent(ctx, runReq, handler); err != nil {
		return AgentStepResult{
			Output:     fullResponse.String(),
			DurationMs: time.Since(startedAt).Milliseconds(),
		}, fmt.Errorf("orchestrator: %w", err)
	}

	// 8. Extract token + cost from result metadata if the adapter
	//    surfaced any. CLI adapters that wrap CLI tools may not —
	//    that's fine, we report zero rather than fabricating.
	tokIn, tokOut := 0, 0
	costUSD := 0.0
	if resultMeta != nil {
		if v, ok := resultMeta["total_cost_usd"].(float64); ok {
			costUSD = v
		}
		if usage, ok := resultMeta["usage"].(map[string]any); ok {
			if v, ok := usage["input_tokens"].(float64); ok {
				tokIn = int(v)
			}
			if v, ok := usage["output_tokens"].(float64); ok {
				tokOut = int(v)
			}
		}
	}

	return AgentStepResult{
		Output:     fullResponse.String(),
		DurationMs: time.Since(startedAt).Milliseconds(),
		CostUSD:    costUSD,
		TokensIn:   tokIn,
		TokensOut:  tokOut,
	}, nil
}

// resolveAgentID looks up the agent row in the author crew with the
// given slug. The workspace_id JOIN guard ensures cross-workspace
// pipeline invocations cannot accidentally (or maliciously) reach
// agents that belong to a different workspace's crew. Returns
// ErrNotFound semantics if no match — the executor surfaces that as
// a step failure with a clear "agent not found in crew" error so the
// pipeline author can fix the slug.
func (r *OrchestratorRunner) resolveAgentID(ctx context.Context, workspaceID, crewID, slug string) (string, error) {
	if workspaceID == "" || crewID == "" || slug == "" {
		return "", errors.New("workspace_id + author_crew_id + agent_slug required")
	}
	var agentID string
	err := r.db.QueryRowContext(ctx,
		`SELECT a.id
		   FROM agents a
		   JOIN crews c ON c.id = a.crew_id
		  WHERE a.crew_id = ? AND a.slug = ? AND a.deleted_at IS NULL
		    AND c.workspace_id = ? AND c.deleted_at IS NULL
		  LIMIT 1`,
		crewID, slug, workspaceID,
	).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("agent slug %q not found in crew %q within workspace", slug, crewID)
	}
	if err != nil {
		return "", err
	}
	return agentID, nil
}
