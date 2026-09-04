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
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/telemetry"
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
//  6. Persist the assistant's turn into that chat — success or
//     failure — and reconcile the chat's message count, so the
//     transcript a watcher saw live survives a reload (#1835).
//  7. Return the assembled assistant text.
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
	// crewRuntime resolves a crew's full PROVISIONED container config
	// (cached image, mounts, caps, env, limits) by crew id, so a script
	// step's cold-crew container launches from the provisioned image —
	// which has the interpreters (python3, …) — rather than the bare base.
	// Nil = fall back to a minimal {ID} config (reuses a warm container;
	// a cold create would use the base image). Injected from cmd_start
	// (which can import internal/api's BuildCrewRuntimeConfig without the
	// import cycle internal/pipeline → internal/api would create).
	crewRuntime func(ctx context.Context, crewID, workspaceID string) (provider.CrewConfig, error)
	// agentRunLock: see OrchestratorRunnerDeps.AgentRunLock's doc. May be
	// wired post-construction via SetAgentRunLock, the same "constructed
	// before the lock exists at boot" pattern api.AssignmentHandler and
	// scheduler.Scheduler use.
	agentRunLock *chatbridge.AgentRunLock
}

// SetAgentRunLock wires the cross-surface per-agent exclusivity lock after
// construction — needed because cmd_start.go builds the OrchestratorRunner
// before chatbridge.Bridge exists. See OrchestratorRunnerDeps.AgentRunLock's
// doc.
func (r *OrchestratorRunner) SetAgentRunLock(l *chatbridge.AgentRunLock) {
	r.agentRunLock = l
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
	// CrewRuntime resolves a crew id → provisioned CrewConfig for script
	// steps (see the field doc on OrchestratorRunner). Optional: nil falls
	// back to a minimal {ID} config.
	CrewRuntime func(ctx context.Context, crewID, workspaceID string) (provider.CrewConfig, error) // optional
	// AgentRunLock is the cross-surface, per-agent exclusivity lock shared
	// with chatbridge.Bridge and api.AssignmentHandler (#2269 follow-up,
	// defect 6). An agent_run routine step and a live assignment/chat turn
	// for the SAME agent both exec into the identical tmux session — see
	// AgentRunLock's own doc for why. Optional: nil is fail-open (not
	// wired), same convention as the other two doors.
	AgentRunLock *chatbridge.AgentRunLock // optional
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
		db:           deps.DB,
		orch:         deps.Orch,
		container:    deps.Container,
		resolver:     deps.Resolver,
		logWriter:    deps.LogWriter,
		convStore:    deps.ConvStore,
		journalE:     deps.Journal,
		logger:       deps.Logger,
		crewRuntime:  deps.CrewRuntime,
		agentRunLock: deps.AgentRunLock,
	}, nil
}

// PrewarmCrew warms the crew's container ahead of a run's first step (#836).
// It goes through the crew-start contract by crew id — no agent resolution
// needed, the runtime image and the declared sidecars are both crew-level
// properties — which is the same idempotent primitive RunStep uses. Concurrent
// prewarms for one crew serialize on the provider's per-crew lock and collapse
// to a single container start. No run row, LLM call, or cost event is produced;
// this only provisions the runtime, off the critical path.
//
// A prewarm brings the crew's sidecars up too: the point of warming is that the
// first step pays nothing, and a first step that finds no database has not been
// warmed. When the crew's config cannot be resolved the starter logs and starts
// from the minimal {ID} config, which reuses a warm container.
func (r *OrchestratorRunner) PrewarmCrew(ctx context.Context, crewID, workspaceID string) error {
	if r.container == nil || crewID == "" {
		return nil
	}
	containerID, cfg, err := r.startCrew(ctx, provider.CrewConfig{ID: crewID}, workspaceID)
	// #1662: prewarm used to discard the container id and register nothing.
	// A container it started was tracked by no subsystem at all — no TTL, so
	// the reaper never saw it; no stats, so the dashboard tile stayed empty —
	// and it ran until crewshipd restarted. No hold: prewarm only warms the
	// runtime, it does not occupy it, so the idle clock starts now.
	//
	// Registration comes BEFORE the error check, because the error can arrive
	// with a live container behind it: the sidecars start after the runtime, so
	// an ErrSidecarStart means "container up, sidecars not". Returning first
	// leaks exactly the container #1662 was about — and prewarm's caller
	// swallows the error into a debug line, so it would leak one per prewarm for
	// as long as the sidecar image stayed broken.
	if r.orch != nil && containerID != "" {
		r.orch.NoteCrewActivity(crewID, containerID, cfg.TTLHours)
		r.orch.RegisterStatsContainer(containerID, crewID, workspaceID)
	}
	if err != nil {
		return err
	}
	return nil
}

// ErrAgentBusy is returned by RunStep when the step's target agent already
// has a live run in progress elsewhere (a chat send, an assignment/@mention
// dispatch, or another routine) holding chatbridge.AgentRunLock — see its
// doc comment for why two concurrent execs into the same agent's tmux
// session corrupt each other. Treated like any other transient step error:
// the routine's own retry/failure policy decides what happens next: this
// package adds no bespoke requeue path of its own (#2269 follow-up, defect
// 6) the way the assignment queue's requeueForLockLoss does.
var ErrAgentBusy = errors.New("pipeline: target agent has a live run in progress elsewhere")

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

	// Cross-surface exclusivity (#2269 follow-up, defect 6): checked right
	// after agentID resolves, before the container/chat cost below is
	// spent on a step that can't run yet — same cheapest-check-first
	// placement api.AssignmentHandler.runAssignment uses for its own
	// TryStart check. Held for the rest of this call via defer.
	if r.agentRunLock != nil {
		if !r.agentRunLock.TryStart(agentID) {
			return AgentStepResult{}, fmt.Errorf("agent %s: %w", agentID, ErrAgentBusy)
		}
		defer r.agentRunLock.End(agentID)
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

	// 3. Start the crew — spawn the container if missing, reuse if already
	//    running, and bring the crew's declared sidecars up either way. This
	//    is literally the same function the chat handler calls
	//    (internal/crewstart); pipelines don't get a separate container pool,
	//    they share the crew's existing runtime.
	// Time the container acquire so `routine logs` can isolate the
	// provision cost from the LLM/tool time in the step's total duration —
	// the quantity the #902 prewarm shortens (#911). A warm hit is near-zero;
	// a cold provision is seconds. Emitted as its own run-keyed journal entry
	// after a successful ensure.
	containerAcquireStart := time.Now()
	// The container config comes from the resolved agent (ChatInfo), the same
	// assembly the chat path uses — including the crew's configured limits, so
	// a cold pipeline run that CREATES the container doesn't pin it to the
	// Docker fallback (8 GiB / 2 CPU) that a hand-rolled config left it with —
	// and including the crew's declared sidecars (#1708).
	stepCfg, cfgErr := info.CrewRuntimeConfig(0, 0)
	if cfgErr != nil {
		r.logger.Warn("pipeline orchestrator runner: crew services unresolved, starting without them",
			"crew_id", info.CrewID, "error", cfgErr)
	}
	containerID, startedCfg, err := r.startCrew(ctx, stepCfg, req.WorkspaceID)
	if err != nil {
		// The runtime container can be UP behind this error — the sidecars are
		// started after it, so ErrSidecarStart means "container running,
		// sidecars not". Registering it before bailing out is what keeps the
		// step's failure from also leaking an untracked container that no
		// reaper will ever stop (#1662). On the success path RunAgent does the
		// same registration further down.
		if r.orch != nil && containerID != "" {
			r.orch.NoteCrewActivity(info.CrewID, containerID, startedCfg.TTLHours)
			r.orch.RegisterStatsContainer(containerID, info.CrewID, req.WorkspaceID)
		}
		return AgentStepResult{}, fmt.Errorf("ensure container: %w", err)
	}
	emitStepContainerReady(ctx, r.journalE, req.WorkspaceID, info.CrewID, containerReady{
		RunID:       req.PipelineRunID,
		PipelineID:  req.PipelineID,
		StepID:      req.StepID,
		ContainerID: containerID,
		DurationMs:  time.Since(containerAcquireStart).Milliseconds(),
		Attempt:     req.Attempt,
	})

	// 4. Synthetic chat session. We mint a fresh chat per step so
	//    journal/audit can join: pipeline_run -> step -> chat ->
	//    agent_run.
	//
	//    Origin ROUTINE is what makes that admission survivable. One chat
	//    per STEP means a five-step nightly routine writes 150 rows a
	//    month into the same table a person's conversations live in, and
	//    `GET /agents/{id}/chats` ordered them all by activity — so the
	//    conversations column showed machine bookkeeping where somebody
	//    was looking for the thread they wrote yesterday. The stamp is the
	//    only thing that lets that column tell the two apart; without it
	//    the classifier's only evidence would be this title, which a
	//    rename can change out from under it (see internal/api/chat_kinds.go).
	//
	//    The title prefers the routine's NAME over its id for the same
	//    reason the origin exists: these rows are read by people now, on
	//    the Routines scope of that column, and "pln_cmtem1pwz000d3e744992"
	//    identifies a routine to a database and to nobody else.
	chatID := generateRunID() // reuse run-id minter; format is fine
	routineLabel := req.PipelineName
	if routineLabel == "" {
		routineLabel = req.PipelineID
	}
	chatTitle := fmt.Sprintf("%s · %s", routineLabel, req.StepID)
	if err := r.resolver.CreateChat(ctx, chatbridge.CreateChatRequest{
		ChatID:      chatID,
		AgentID:     agentID,
		WorkspaceID: req.WorkspaceID,
		Title:       chatTitle,
		Origin:      "ROUTINE",
	}); err != nil {
		// Non-fatal: a missing chat row degrades the audit trail
		// but doesn't break the run. Log and continue.
		r.logger.Warn("pipeline orchestrator runner: create chat failed",
			"error", err, "pipeline_id", req.PipelineID, "step_id", req.StepID)
	}

	// 5. Persist the rendered prompt as the user message so the
	//    chat's conversation history shows what the pipeline asked.
	//
	// AgentID is stamped because conversation.Store.Search filters on it as its
	// isolation boundary — a turn written without one is in the chat but
	// invisible to the agent's own recall.
	promptPersisted := false
	if r.convStore != nil {
		if err := r.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateRunID(),
			AgentID:   agentID,
			Role:      conversation.RoleUser,
			Content:   req.Prompt,
			Timestamp: time.Now().UTC(),
		}); err != nil {
			r.logger.Warn("pipeline orchestrator runner: persist step prompt failed",
				"error", err, "chat_id", chatID, "step_id", req.StepID)
		} else {
			promptPersisted = true
		}
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
	//
	// CLIAdapter therefore is not a per-call override: the converter
	// sources it from info.CLIAdapter, which is the value the old
	// `cliAdapter := info.CLIAdapter` resolved to.
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
	runReq := info.ToAgentRunRequest(chatbridge.AgentRunOverrides{
		ChatID:      chatID,
		ContainerID: containerID,
		UserMessage: req.Prompt,
		LLMModel:    llmModel,
		TimeoutSecs: timeoutSecs,
		// Previously omitted — pipeline-launched agents silently lost
		// their resource limits. Pass the agent's configured limits.
		MemoryMB: info.MemoryMB,
		CPUs:     info.CPUs,
	})

	// 7. Run with buffering handler that captures text + result
	//    events. The "result" event carries usage metadata
	//    (token counts, cost) that we surface as AgentStepResult
	//    so the executor's run summary is accurate.
	startedAt := time.Now()

	var logBuf *logcollector.OutputBuffer
	if r.logWriter != nil {
		logBuf = logcollector.NewOutputBuffer(r.logWriter, info.CrewID, info.AgentSlug)
		defer logBuf.Close()
	}

	handler, acc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
		LogBuf:            logBuf,
		AgentSlug:         info.AgentSlug,
		AccumulateText:    true,
		CaptureResultMeta: true,
	})

	// partAcc assembles the ordered structured segments of the agent's turn
	// (text / thinking / tool calls / tool results) from the same normalized
	// event stream the chat bridge feeds its own accumulator. Reload has to
	// render what the live watcher saw: #1831 put this run's tool activity on
	// screen, and a turn flattened to Content alone comes back as one
	// undifferentiated blob with the tools missing. Accumulating
	// unconditionally keeps one handler chain rather than a second conditional
	// wrapper; the cost is one slice append per event, next to the log-buffer
	// write already on that path.
	partAcc := conversation.NewPartAccumulator()
	finalHandler := func(ev orchestrator.AgentEvent) {
		partAcc.Add(ev.Type, ev.Content, ev.Metadata)
		handler(ev)
	}

	// Sub-span capture — the leaf of the drillable run-trace tree. When this
	// is a real routine step (run_id + step_id present), wrap the buffering
	// handler with a recorder that pairs the agent's tool_use→tool_result
	// events into RunAgentSpans. Each completed span is persisted to the
	// journal (run.agent_span, keyed back to this run via trace_id) and
	// mirrored as an OTEL child of the step span carried on ctx (run → step →
	// tool). A step with no tool calls produces zero spans and leaves the hot
	// path byte-identical to before. ctx already carries StartRoutineStepSpan
	// from the executor, so the OTEL children nest correctly.
	var recorder *orchestrator.AgentSpanRecorder
	if req.PipelineRunID != "" && req.StepID != "" {
		runID, stepID := req.PipelineRunID, req.StepID
		wsID, crewID := req.WorkspaceID, info.CrewID
		recorder = orchestrator.NewAgentSpanRecorder(runID, stepID, func(span orchestrator.RunAgentSpan) {
			emitRunAgentSpan(ctx, r.journalE, wsID, crewID, span)
			telemetry.RecordRunAgentToolSpan(ctx, span.Kind, span.Name, span.Seq,
				span.StartedAt, span.DurationMs, span.Status, span.Attributes)
		})
		inner := finalHandler
		finalHandler = func(ev orchestrator.AgentEvent) {
			recorder.Observe(ev)
			inner(ev)
		}
	}

	runErr := r.orch.RunAgent(ctx, runReq, finalHandler)
	// Surface volume-bounding so operators can tell a sparse trace from a
	// throttled one (a chatty agent hitting the per-step cap / detail cap).
	if recorder != nil && (recorder.Dropped() > 0 || recorder.Truncated() > 0) {
		r.logger.Warn("agent sub-span volume bounded",
			"run_id", req.PipelineRunID, "step_id", req.StepID,
			"dropped", recorder.Dropped(), "truncated", recorder.Truncated())
	}
	if runErr != nil {
		// Partial-usage on error (#1426, 3.4). A cancelled or killed stream
		// may still have surfaced usage metadata for the tokens already
		// spent; report it so a cancel mid-agent-step records the real
		// partial cost instead of $0 (the cost cap + paymaster otherwise
		// undercount every interrupted run).
		costUSD, tokIn, tokOut := orchestrator.ParseResultUsage(acc.ResultMeta())
		// A failed step keeps whatever the agent managed to say. An in-band
		// failure — the CLI exits 0 and its own terminal event reports the turn
		// failed — is the failure shape that usually DID produce text, and the
		// chat is exactly where someone goes to find out why the step went red.
		// This mirrors the WebSocket path's persistInBandFailureTurn.
		r.recordChatTurn(ctx, chatID, agentID, acc.Text(), partAcc.Parts(), promptPersisted)
		return AgentStepResult{
			Output:     acc.Text(),
			DurationMs: time.Since(startedAt).Milliseconds(),
			CostUSD:    costUSD,
			TokensIn:   tokIn,
			TokensOut:  tokOut,
		}, fmt.Errorf("orchestrator: %w", runErr)
	}

	// The step succeeded — persist the reply so the chat still holds it after
	// the browser reloads (#1835). Before this, a routine step wrote the prompt
	// and dropped the answer, which was invisible until #1823/#1831 made these
	// runs watchable and the text started disappearing in front of people.
	r.recordChatTurn(ctx, chatID, agentID, acc.Text(), partAcc.Parts(), promptPersisted)

	// 8. Extract token + cost from result metadata if the adapter
	//    surfaced any. CLI adapters that wrap CLI tools may not —
	//    that's fine, we report zero rather than fabricating.
	costUSD, tokIn, tokOut := orchestrator.ParseResultUsage(acc.ResultMeta())

	// Local-model observability (#974/U3): a run on a local ("ollama/…")
	// endpoint costs $0 and its traffic never reaches the sidecar's cost path
	// (that only tags known cloud hosts), so without this it produces no
	// cost_ledger row and `paymaster` shows zero tokens for local work. Record
	// the parsed token counts here at $0. Gated to local models so cloud runs —
	// whose ledger rows come from the sidecar — are never double-counted.
	if strings.HasPrefix(llmModel, "ollama/") && (tokIn > 0 || tokOut > 0) {
		if _, err := paymaster.Record(ctx, r.db, r.journalE, paymaster.Call{
			Scope:        paymaster.Scope{WorkspaceID: req.WorkspaceID, CrewID: info.CrewID, AgentID: agentID},
			Provider:     "ollama",
			Model:        strings.TrimPrefix(llmModel, "ollama/"),
			InputTokens:  int64(tokIn),
			OutputTokens: int64(tokOut),
			CostUSD:      0,
			BillingMode:  paymaster.BillingMetered,
			Tags:         map[string]any{"source": "local_model", "run_id": req.PipelineRunID},
		}); err != nil {
			r.logger.Warn("record local-model usage", "run_id", req.PipelineRunID, "error", err)
		}
	}

	return AgentStepResult{
		Output:     acc.Text(),
		DurationMs: time.Since(startedAt).Milliseconds(),
		CostUSD:    costUSD,
		TokensIn:   tokIn,
		TokensOut:  tokOut,
	}, nil
}

// persistTurnDeadline bounds the detached write recordChatTurn falls back to
// when the step's own context is already done. Same 5 seconds the chat bridge
// gives its cancelled-run persist (chatbridge.HandleChatMessage's cleanCtx).
const persistTurnDeadline = 5 * time.Second

// persistContext returns a context safe to write the turn under.
//
// A cancelled or timed-out step leaves ctx already done, and both
// conversation.Store.Append and the message-count IPC refuse a done context —
// so persisting under it would drop exactly the text the live watcher just
// saw, on the one path where losing it is most visible. Detaching from the
// cancellation while KEEPING the values (trace context, deadlines carried as
// values) is what context.WithoutCancel is for; the bridge does the same thing
// with a bare Background because it predates that API.
//
// The healthy case allocates nothing and returns the caller's own context, so
// a normal step's persist is still bounded by the step's deadline.
func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), persistTurnDeadline)
}

// persistAssistantTurn writes the agent's reply into the step's chat and
// reports whether anything landed. A run that produced neither text nor parts
// (a refusal the adapter swallowed, an exec that died before first token)
// writes nothing rather than an empty bubble.
//
// Best-effort by design: a storage failure is logged, never returned. The
// pipeline runs on the step's RESULT, and reddening a green step because its
// transcript could not be written would trade a cosmetic loss for a real one.
func (r *OrchestratorRunner) persistAssistantTurn(ctx context.Context, chatID, agentID, text string, parts []conversation.Part) bool {
	if r.convStore == nil || (text == "" && len(parts) == 0) {
		return false
	}
	ctx, cancel := persistContext(ctx)
	defer cancel()
	if err := r.convStore.Append(ctx, chatID, conversation.Message{
		ID:        generateRunID(),
		AgentID:   agentID,
		Role:      conversation.RoleAssistant,
		Content:   text,
		Parts:     parts,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		r.logger.Warn("pipeline orchestrator runner: persist step reply failed",
			"error", err, "chat_id", chatID)
		return false
	}
	return true
}

// recordChatTurn closes out the step's chat: it persists the agent's reply and
// then reconciles the chat's derived message_count with what the chat actually
// holds.
//
// The count is incremented per message that landed, not as the bridge's and
// scheduler's coupled "+2 for the pair". Those two can couple it because in a
// chat turn the prompt and the reply always land together; a routine step can
// persist a prompt and then fail before the agent says anything, and a chat
// reporting "2 messages" while holding one is the same class of lie #1835 is
// about. Zero messages written means no call at all — IncrementMessageCount
// also stamps last_activity_at, and a step that stored nothing did not make the
// chat active.
//
// # This turn is persisted and deliberately NOT notified
//
// The bridge follows its own Append with notifyReply, projecting "your agent
// replied" into the unified inbox (internal/chatnotify). This path does not,
// and that is a decision rather than an omission:
//
//   - Nobody asked. The inbox item's premise is that a human sent a message,
//     walked away, and would otherwise miss the answer. A routine step is the
//     machine talking to itself on a schedule — there is no waiting human whose
//     question went unanswered, so "an agent replied" is not news.
//   - The dedupe that makes chat replies survivable would not engage. chatnotify
//     collapses repeat replies onto ONE unread item per (user, chat), keyed
//     "chat_reply_<chat>_<user>". A routine step mints a FRESH chat per step per
//     attempt (step 4 in RunStep), so each run would produce a new key and the
//     items would stack rather than collapse: a fifteen-minute routine with
//     three agent steps is 288 inbox items a day, per recipient.
//   - Routines already have a notification surface built for them (the
//     routine.* categories in internal/notify, #843), which knows about runs,
//     outcomes and failures. A second, blinder channel that fires once per step
//     regardless of outcome would bury that signal, not add to it.
//   - The scheduler — the other unattended dispatch path — has persisted its
//     replies without notifying since it was written. Keeping the two
//     unattended paths saying the same thing is worth more than either of them
//     being individually clever.
//
// This was written down because it used to be true by accident, twice, and both
// accidents were the kind that quietly stop holding: notifyReply is a *Bridge*
// method and this path does not use the Bridge, and a step's chat was created
// with no user at all (created_by NULL, no participants) so chatnotify would
// have found no recipients even if it were called.
//
// The second accident is gone. chatnotify now classifies the chat with
// chatkind and returns before resolving a recipient for anything it calls
// machine — a routine step, a cron or webhook dispatch, an issue's mission
// chat, a delegation — so an owned routine chat raises nothing either, which is
// what TestNotify_MachineChatKindsRaiseNothingEvenWithAnOwner pins. That
// mattered sooner than the comment expected: `POST /agents/{id}/chats` takes a
// caller-supplied origin AND stamps created_by, so an owned ROUTINE chat is one
// authenticated request, not a hypothetical future migration.
//
// The FIRST accident still stands, and this comment is still the only thing
// holding it: nothing stops a later author from routing this path through the
// Bridge. The reasons above are why they should not.
func (r *OrchestratorRunner) recordChatTurn(ctx context.Context, chatID, agentID, text string, parts []conversation.Part, promptPersisted bool) {
	written := 0
	if promptPersisted {
		written++
	}
	if r.persistAssistantTurn(ctx, chatID, agentID, text, parts) {
		written++
	}
	if written == 0 {
		return
	}
	countCtx, cancel := persistContext(ctx)
	defer cancel()
	if err := r.resolver.IncrementMessageCount(countCtx, chatID, written); err != nil {
		r.logger.Warn("pipeline orchestrator runner: message count update failed",
			"error", err, "chat_id", chatID, "delta", written)
	}
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
