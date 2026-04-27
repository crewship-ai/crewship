package hooks

import (
	"context"
	"sync"
	"time"
)

// SubagentHandler is implemented by the orchestrator and injected into this
// package at startup via SetSubagentHandler. Keeping it abstract lets the
// hooks package stay free of orchestrator / LLM / provider dependencies
// (which would pull a large chunk of the codebase into every test that
// imports hooks).
//
// The orchestrator's implementation typically:
//
//  1. Picks a short-lived agent defined by handler_config.agent_id or
//     spawns an ephemeral one from a template.
//  2. Passes the event context as the agent's task prompt.
//  3. Parses the agent's structured response into Result.Outcome.
//
// Full integration lands in a follow-up commit; this file just pins the
// contract so dispatcher.go can compile today.
type SubagentHandler interface {
	Run(ctx context.Context, hook Hook, ec EventContext) (Result, error)
}

var (
	subagentMu      sync.RWMutex
	subagentHandler SubagentHandler
)

// SetSubagentHandler wires the orchestrator's implementation into the
// hooks package. Call once at server startup; subsequent calls overwrite
// the previous handler, which is useful in tests but not in production.
func SetSubagentHandler(h SubagentHandler) {
	subagentMu.Lock()
	subagentHandler = h
	subagentMu.Unlock()
}

// subagentHandlerDispatch is the thin shim the dispatcher calls. When no
// handler has been registered it returns ErrSubagentHandlerNotConfigured
// so the caller can surface the misconfiguration in the journal without
// bringing down the whole dispatch chain.
func subagentHandlerDispatch(ctx context.Context, h Hook, ec EventContext) (Result, error) {
	subagentMu.RLock()
	handler := subagentHandler
	subagentMu.RUnlock()

	if handler == nil {
		return Result{
			Outcome: OutcomeError,
			Message: "subagent handler not configured",
			Latency: 0,
		}, ErrSubagentHandlerNotConfigured
	}
	start := time.Now()
	res, err := handler.Run(ctx, h, ec)
	if res.Latency == 0 {
		res.Latency = time.Since(start)
	}
	return res, err
}
