// Package behaviorhook wires the F4.2 behavior evaluator into the
// hooks.EventPostToolCall dispatch path with a per-crew sampling
// frequency. The hook handler is *not* registered as a row in
// hooks_config — F4.2 is a platform feature, not an operator-authored
// hook — but it shares the same BlockedError contract so the existing
// dispatcher's Block semantics work without modification.
//
// Usage from the orchestrator's tool-call site:
//
//	if h := behaviorhook.Get(); h != nil {
//	    if be, ok := h.MaybeEvaluate(ctx, ec); ok && be != nil {
//	        return be // bubbles up; caller treats as a tool-call abort
//	    }
//	}
//
// MaybeEvaluate is the sampling gate: it returns (nil, false) for
// most calls (the bulk of tool calls are NOT sampled) and only fires
// the LLM when the per-crew counter wraps around the sampling
// interval. The default sampling rate is 1 in 5 (configurable via
// SetSampleEvery).
//
// Concurrency: Hook is safe for concurrent use. The per-crew counter
// is keyed by crew_id; a sync.Map is good enough for the hot path
// (low cardinality, lock-free reads).
//
// Persistence: this package does NOT write to the DB. The keeper API
// handler in internal/api/keeper_phase2.go (C.9) is the persistence
// layer; this hook is the *trigger*. We deliberately keep the
// evaluator call here synchronous + low-rate so blocking semantics
// (mode=block × strict/guided × DENY) can interrupt the next tool
// call deterministically.
package behaviorhook

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// defaultSampleEvery: fire the evaluator every Nth tool call per
// crew. PRD §6 F4.2 specifies "default every 5th call". Tunable per
// crew via SetSampleEvery (future); MVP keeps it global.
const defaultSampleEvery = 5

// Hook bundles the evaluator + policy resolver + sampling state.
type Hook struct {
	ev          *gatekeeper.BehaviorEvaluator
	policy      *policy.Resolver
	logger      *slog.Logger
	sampleEvery atomic.Int64
	mu          sync.Mutex
	counters    map[string]*atomic.Int64 // crew_id -> tool-call counter
}

// New constructs a Hook. ev + policyResolver are required; nil-checks
// surface as ErrNotConfigured at MaybeEvaluate time so callers can
// keep using the hook as a soft dependency.
func New(ev *gatekeeper.BehaviorEvaluator, policyResolver *policy.Resolver, logger *slog.Logger) *Hook {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hook{
		ev:       ev,
		policy:   policyResolver,
		logger:   logger,
		counters: map[string]*atomic.Int64{},
	}
	h.sampleEvery.Store(defaultSampleEvery)
	return h
}

// SetSampleEvery overrides the per-crew sampling cadence. Pass <=0 to
// disable sampling entirely (the hook becomes a no-op). Useful for
// load tests + the future per-crew tuning surface.
func (h *Hook) SetSampleEvery(every int64) {
	if every < 0 {
		every = 0
	}
	h.sampleEvery.Store(every)
}

// ErrNotConfigured is returned when MaybeEvaluate is called on a Hook
// whose dependencies (evaluator or policy resolver) were nil at
// construction. The caller should treat this as "skip behavior
// monitoring" rather than "abort the tool call".
var ErrNotConfigured = errors.New("behaviorhook: evaluator or policy resolver not configured")

// MaybeEvaluate is the sampling gate. Returns:
//
//	(nil, false)              — not sampled this call (the common case)
//	(*BlockedError, true)     — sampled + DENY in block mode + strict/guided
//	(nil, true)               — sampled + LLM verdict was non-blocking
//	(nil, true) + err logged  — sampled + evaluator hit an error (fail-soft)
//
// The boolean indicates whether the hook fired the evaluator at all;
// callers can log it for telemetry without inferring from a nil error.
//
// The block path returns a *hooks.BlockedError so the orchestrator
// can reuse its existing errors.As(*hooks.BlockedError) detection
// without a parallel error type — keeps the "what does the agent see"
// surface uniform across operator-authored hooks and platform hooks.
func (h *Hook) MaybeEvaluate(ctx context.Context, ec hooks.EventContext) (*hooks.BlockedError, bool) {
	if h.ev == nil || h.policy == nil {
		return nil, false
	}
	every := h.sampleEvery.Load()
	if every <= 0 {
		return nil, false
	}

	counter := h.counterFor(ec.CrewID)
	count := counter.Add(1)
	if count%every != 0 {
		return nil, false
	}

	// Resolve policy (cached per crew for cacheTTL by the resolver).
	p, err := h.policy.Resolve(ctx, ec.CrewID)
	if err != nil {
		h.logger.Warn("behaviorhook: policy resolve failed; skipping sample",
			"crew_id", ec.CrewID, "error", err)
		return nil, true
	}

	res, err := h.ev.Evaluate(ctx, gatekeeper.BehaviorReviewRequest{
		WorkspaceID:     ec.WorkspaceID,
		CrewID:          ec.CrewID,
		AgentName:       ec.AgentID,
		CrewName:        ec.CrewID, // crew name not in EventContext; ID is fine for prompt
		BehaviorMode:    p.BehaviorMode,
		AutonomyLevel:   p.AutonomyLevel,
		ToolName:        ec.ToolName,
		ToolArgsSnippet: payloadSnippet(ec.Payload),
	})
	if err != nil {
		h.logger.Warn("behaviorhook: evaluator error; treating as non-blocking",
			"crew_id", ec.CrewID, "agent_id", ec.AgentID, "tool", ec.ToolName, "error", err)
		return nil, true
	}

	if !res.ShouldBlock {
		return nil, true
	}

	be := &hooks.BlockedError{
		HookID: "behavior_monitor",
		Event:  hooks.EventPostToolCall,
		Result: hooks.Result{
			Outcome: hooks.OutcomeBlock,
			Message: fmt.Sprintf("F4.2 behavior monitor: %s", res.Reason),
		},
	}
	return be, true
}

// counterFor returns the per-crew counter, creating it lazily under
// the package mutex. Counter increments are lock-free via
// atomic.Int64; only the initial map insert is serialised.
func (h *Hook) counterFor(crewID string) *atomic.Int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.counters[crewID]; ok {
		return c
	}
	c := new(atomic.Int64)
	h.counters[crewID] = c
	return c
}

// payloadSnippet renders the EventContext.Payload as a short JSON
// fragment for the prompt. Bounded by truncateSnippet in the
// evaluator's BehaviorInput renderer, so we just need to produce
// something serialisable here.
func payloadSnippet(p map[string]any) string {
	if len(p) == 0 {
		return ""
	}
	// Avoid encoding/json here to keep this package dependency-free
	// of heavy serialisation paths — a flat key=val rendering is enough
	// for the LLM to spot anti-patterns (the prompt only needs a
	// fingerprint of the args, not a parseable round-trip).
	out := "{"
	first := true
	for k, v := range p {
		if !first {
			out += ", "
		}
		out += fmt.Sprintf("%q: %v", k, v)
		first = false
		if len(out) > 480 {
			break
		}
	}
	out += "}"
	return out
}

// ---------- Global singleton (parallels hooks.SetSubagentHandler) ---

var (
	globalMu sync.RWMutex
	global   *Hook
)

// Set installs the process-wide Hook. Called once during server
// startup; subsequent calls overwrite (test-friendly).
func Set(h *Hook) {
	globalMu.Lock()
	global = h
	globalMu.Unlock()
}

// Get returns the installed Hook or nil if none is configured. Callers
// in the orchestrator hot path check for nil before invoking.
func Get() *Hook {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}
