package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/lookout"
)

// LLMRunner is a lightweight AgentRunner that satisfies the
// pipeline.AgentRunner contract by talking directly to internal/llm
// providers. Each step renders into a single LLM Complete() call:
// the agent's stored system prompt + the rendered step prompt as a
// user turn.
//
// This was the original runner shipped in #281; #282 swapped it for
// OrchestratorRunner so production routines run inside the agent's
// real CLI adapter (Claude Code / Codex / etc.) and inherit the
// agent's full toolset. The trade-off is that OrchestratorRunner
// requires Docker + a provisioned crew container + an installed
// adapter — which makes the **eval suite untestable on a workstation
// without the full container stack**.
//
// LLMRunner is restored as the OPT-IN runner for two specific cases:
//
//  1. Eval scenarios. Cross-tier consistency tests (Haiku-vs-Opus
//     same gate-pass) are about the LLM-step contract, not the full
//     agent loop — skills/MCP/memory are deliberately out of scope
//     for what an eval scenario asserts. LLMRunner exercises exactly
//     the surface evals care about.
//  2. CI / smoke runs. `--no-docker` boot still produces a runnable
//     server you can hit `crewship routine run` against, instead of
//     503-ing.
//
// Selected at boot via `--no-docker` (auto) or
// `CREWSHIP_PIPELINE_RUNNER=llm_direct` (explicit override). When
// unset, OrchestratorRunner remains the default — production
// behaviour is unchanged.
//
// Trade-offs vs. the OrchestratorRunner path:
//
//   - Pro: works without container provisioning, no sidecar boot, no
//     chat session lifecycle. Cost per step ~= one LLM round-trip.
//   - Pro: paymaster + lookout + telemetry middleware applies (we
//     wrap the raw provider before returning), so cost ledger and
//     guardrails work the same way as direct chat.
//   - Con: pipeline steps DO NOT have access to the agent's skills,
//     MCP tools, or memory. The system prompt is loaded but tool
//     loops are out of scope. Routines that need real tool
//     invocation (gmail fetch, terraform apply) must use
//     OrchestratorRunner.
//   - Con: Anthropic only today. The tier resolver still picks model
//     names freely; this runner clamps any non-anthropic adapter to
//     the workspace's anthropic key. OpenAI + Ollama support is a
//     small follow-up — the wrappers exist, just need the credential
//     lookup branch.
//
// Author-crew-context contract is preserved: AgentStepRequest
// carries AuthorCrewID + AgentSlug, and the system prompt is
// resolved against that pair. Crew B invoking Crew A's pipeline
// gets Crew A's persona, not Crew B's.
type LLMRunner struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewLLMRunner constructs an LLMRunner against the supplied DB +
// journal emitter. The journal is needed because llm.Middleware
// emits cost-ledger entries through it; passing a no-op journal is
// fine for tests but production wiring should pass the real writer.
func NewLLMRunner(db *sql.DB, journalEmitter journal.Emitter, logger *slog.Logger) *LLMRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &LLMRunner{db: db, logger: logger, journal: journalEmitter}
}

// errNoAnthropicCred is returned when the workspace has no active
// Anthropic credential. We surface this as a step failure rather
// than crashing the pipeline; the caller will see "FAILED at step X
// because no anthropic credential" and can either provision one or
// switch to a different tier.
var errNoAnthropicCred = errors.New("no active Anthropic credential in workspace (LLMRunner mode requires one — set SEED_ANTHROPIC_API_KEY before seeding, or POST /api/v1/credentials with provider=ANTHROPIC type=API_KEY)")

// RunStep satisfies pipeline.AgentRunner. Resolves the agent's
// stored system prompt, fetches the workspace Anthropic credential,
// wraps the provider with the full middleware stack, and runs a
// single Complete() against (system, prompt). Output is the
// assistant's response text; cost + token counts come from the
// llm.Response that the middleware records in the paymaster ledger.
func (r *LLMRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	systemPrompt, authorAgentID, err := r.resolveAgentSystemPrompt(ctx, req.AuthorCrewID, req.AgentSlug)
	if err != nil {
		// "Agent not found" is a save-time bug we couldn't catch
		// (slug existed at save, agent deleted between save+run).
		// Surface a clean error so the executor can report it.
		return AgentStepResult{}, err
	}

	provider, err := r.providerForWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		return AgentStepResult{}, fmt.Errorf("LLMRunner: provider: %w", err)
	}

	// Attach lookout scope so paymaster middleware can attribute
	// cost. Without this, paymaster.Scope.WorkspaceID is empty and
	// Complete returns "paymaster: workspace_id required". The
	// HTTP handler chain attaches scope on inbound API requests;
	// the pipeline executor calls RunStep from a goroutine that
	// inherits the request context but the inner Complete call
	// re-derives scope, so we re-attach explicitly here. This
	// mirrors how the orchestrator's goroutines wrap their inner
	// LLM calls.
	//
	// AgentID is the resolved AUTHOR agent's id — the agent the
	// step actually runs AS. Cross-crew invocation: Crew B
	// triggering Crew A's routine bills Crew A's agent, not
	// Crew B's invoker. This matches the cross-crew reuse contract
	// the orchestrator runner already enforces and keeps cost
	// analytics meaningful when one crew owns a heavily-shared
	// routine fleet.
	scope := lookout.Scope{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.AuthorCrewID,
		AgentID:     authorAgentID,
	}
	ctx = lookout.WithScope(ctx, scope)

	// Per-routine input-guard action override. The DSL plumbs through
	// AgentStepRequest.InputGuardAction (empty leaves the default).
	// We pass the string through WithAction; the helper coerces empty
	// to no-op so we don't have to branch here.
	if req.InputGuardAction != "" {
		ctx = lookout.WithAction(ctx, lookout.GuardAction(req.InputGuardAction))
	}

	// Build the LLM request. We pass the resolved system prompt
	// + the pipeline's rendered prompt as a single user turn.
	// Multi-turn pipelines come from chaining steps in the DSL,
	// not from in-step conversation. This keeps each step
	// stateless, which is the simplest model that matches the
	// pipeline runtime's "deterministic per step" promise.
	llmReq := llm.Request{
		Model:     req.Model,
		System:    systemPrompt,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: req.Prompt}},
		MaxTokens: 4096,
	}
	resp, err := provider.Complete(ctx, llmReq)
	if err != nil {
		return AgentStepResult{}, fmt.Errorf("LLMRunner: complete: %w", err)
	}
	if resp == nil {
		// Defensive: a provider that returns (nil, nil) is a bug
		// upstream. Bail with a clean error rather than panicking
		// when the executor reads resp.Content.
		return AgentStepResult{}, fmt.Errorf("LLMRunner: provider returned nil response with no error")
	}

	// Cost is reported by the paymaster middleware via journal
	// entries; we don't double-count here. The result fields are
	// best-effort summaries for the executor's run report.
	return AgentStepResult{
		Output:    resp.Content,
		TokensIn:  resp.InputToks,
		TokensOut: resp.OutputToks,
	}, nil
}

// resolveAgentSystemPrompt looks up the agent's persona/system
// prompt by (author_crew_id, agent_slug) AND returns the resolved
// agent's row id. The id is returned alongside the prompt so the
// caller can attribute paymaster cost to the AUTHOR agent (the
// agent the routine actually runs as) rather than the invoker —
// otherwise cross-crew or delegated runs bill the wrong agent.
//
// Returns ErrNotFound when the slug doesn't resolve in the author
// crew. Empty system prompt is allowed — many lightweight agents
// are persona-free.
func (r *LLMRunner) resolveAgentSystemPrompt(ctx context.Context, crewID, slug string) (systemPrompt, agentID string, err error) {
	if crewID == "" || slug == "" {
		return "", "", fmt.Errorf("crew_id + agent_slug required")
	}
	var (
		prompt sql.NullString
		id     string
	)
	err = r.db.QueryRowContext(ctx, `
SELECT a.id, a.system_prompt
FROM agents a
WHERE a.crew_id = ?
  AND a.slug = ?
  AND a.deleted_at IS NULL
LIMIT 1`, crewID, slug).Scan(&id, &prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("agent slug %q not found in crew %q", slug, crewID)
	}
	if err != nil {
		return "", "", fmt.Errorf("LLMRunner: resolve agent: %w", err)
	}
	if !prompt.Valid {
		return "", id, nil
	}
	return prompt.String, id, nil
}

// providerForWorkspace returns a middleware-wrapped Anthropic
// Provider for the given workspace. Mirrors the pattern in
// internal/api/crew_ai.go's getLLMProvider — same query, same
// middleware stack, same error semantics. Kept as a separate
// implementation here (rather than calling into api/) to avoid an
// import cycle: api depends on pipeline, not the other way around.
//
// Phase 1.5 will extend this to dispatch on tier.AdapterModel
// (claude vs openai vs ollama). For MVP, every step lands on
// Anthropic — the tier resolver's model name passes through to
// the Anthropic SDK which routes by model id (haiku-4-5 vs
// sonnet-4-6 vs opus-4-7).
func (r *LLMRunner) providerForWorkspace(ctx context.Context, workspaceID string) (llm.Provider, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("workspace_id required")
	}
	// Match either API_KEY (Messages API) or AI_CLI_TOKEN (OAuth
	// for Claude Code CLI). Both are valid Anthropic auth surfaces;
	// the seed picks AI_CLI_TOKEN when SEED_ANTHROPIC_API_KEY starts
	// with sk-ant-oat (OAuth flow), otherwise API_KEY. We accept
	// both here and let the Anthropic SDK reject if a given token
	// doesn't carry Messages-API scope — the runtime error is clearer
	// than a "no credential" message would be when one is actually
	// present.
	//
	// Order matters: DESC by created_at so the LATEST rotated key
	// wins when an operator has rotated credentials and both rows
	// are still ACTIVE. The seed only ever creates one row per
	// workspace so this only matters in rotated workspaces — but
	// in those cases ASC silently picks a stale key, which we
	// caught in CodeRabbit review of #285.
	var encryptedValue string
	err := r.db.QueryRowContext(ctx, `
SELECT encrypted_value FROM credentials
WHERE workspace_id = ?
  AND provider = 'ANTHROPIC'
  AND type IN ('API_KEY', 'AI_CLI_TOKEN')
  AND status = 'ACTIVE'
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1`, workspaceID).Scan(&encryptedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoAnthropicCred
	}
	if err != nil {
		return nil, fmt.Errorf("query credential: %w", err)
	}
	plain, err := encryption.Decrypt(encryptedValue)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	// Wrap with the full middleware stack so paymaster ledger
	// rows land for each Complete() call. Without this wrap the
	// run is invisible to cost analytics.
	return llm.Middleware(llm.NewAnthropic(plain), r.journal, r.db), nil
}
