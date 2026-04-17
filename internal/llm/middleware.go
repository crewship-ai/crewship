package llm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/lookout"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/telemetry"
)

// Middleware wraps base with the full LLM call stack:
//
//	telemetry  ->  paymaster  ->  lookout  ->  caching (future)  ->  base
//
// Each layer matches the paymaster.LLMCaller signature so they compose as
// plain function wrappers. The returned Provider preserves the original
// Name() for routing and fan-out on the caller side.
//
// Layer order is deliberate and documented here because getting it wrong
// produces subtle bugs that only show up under load:
//
//  1. telemetry is outermost so the span covers the full request (budget
//     check, guardrails, cache lookup, network). An SRE looking at a slow
//     trace must see every contributor, not just the last hop.
//  2. paymaster comes next so the pre-call budget check can refuse a
//     request before we've paid for a cache lookup. The cost ledger also
//     records the final bill here, outside the guardrail layer so
//     sanitization time is not counted toward "provider latency".
//  3. lookout scans the messages before they hit the provider. Running it
//     INSIDE paymaster is important: if lookout blocks the call, no
//     ledger row is written (because next.Call is never invoked). A
//     blocked call is not a billable call.
//  4. caching (not yet implemented at this layer — Anthropic's own prompt
//     caching is handled wire-side in anthropic.go) would go here so a
//     cache hit bypasses both lookout and the provider but still flows
//     through paymaster for the hit-event accounting.
//  5. base is the raw Provider.
//
// Stream() is passed through unchanged for now. Streamed calls bypass the
// middleware because the paymaster ledger row depends on final token
// counts which arrive in the final `message_delta` event — wiring that
// through the sync LLMCaller shape would require a streaming variant of
// CallResponse. That's deferred; callers who use Stream today still pay
// through the orchestrator-level accounting that predates this middleware.
func Middleware(base Provider, j journal.Emitter, db *sql.DB) Provider {
	if base == nil {
		return nil
	}
	// Build the chain bottom-up so each wrap sees its inner caller as a
	// concrete paymaster.LLMCaller, not a hand-rolled struct.
	var caller paymaster.LLMCaller = providerCaller{p: base}
	caller = lookoutCaller(caller, j)
	caller = paymaster.Middleware(caller, j, db)
	caller = telemetry.LLMMiddleware(caller)

	return &wrappedProvider{base: base, caller: caller}
}

// wrappedProvider is the Provider returned by Middleware. Complete() runs
// through the full caller stack; Stream() delegates to the underlying
// provider verbatim (see notes on Stream above).
type wrappedProvider struct {
	base   Provider
	caller paymaster.LLMCaller
}

func (w *wrappedProvider) Name() string { return w.base.Name() }

// Complete routes the request through the caller chain. The request is
// passed as the opaque CallRequest.Inputs field — the innermost
// providerCaller knows the concrete type and unpacks it.
func (w *wrappedProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	scope, _ := paymasterScopeFromContext(ctx)
	callReq := paymaster.CallRequest{
		Scope:    scope,
		Provider: w.base.Name(),
		Model:    req.Model,
		Inputs:   req,
	}
	resp, err := w.caller.Call(ctx, callReq)
	if err != nil {
		return nil, err
	}
	out, ok := resp.Output.(*Response)
	if !ok || out == nil {
		return nil, errors.New("llm/middleware: inner caller returned no Response")
	}
	return out, nil
}

// Stream applies the lookout input guard synchronously before delegating
// to the base provider. Paymaster budget accounting and telemetry spans
// still aren't wired into the streaming path (that needs a streaming-
// aware ledger variant — tracked as a follow-up), but refusing to pass
// a prompt-injection attempt through to the LLM is the minimum the
// security layer has to do. Without this pre-call guard, any caller
// that picks Stream over Complete would silently bypass every guardrail
// — the classic "optional security" trap CodeRabbit flagged.
func (w *wrappedProvider) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	for _, m := range req.Messages {
		if m.Role != RoleUser && m.Role != RoleTool {
			continue
		}
		res := lookout.ScanInput(m.Content)
		if res.Verdict == lookout.VerdictBlock && len(res.Findings) > 0 {
			return nil, &lookout.BlockedError{Direction: "input", Finding: res.Findings[0]}
		}
	}
	return w.base.Stream(ctx, req, handler)
}

// providerCaller is the innermost layer: it unpacks the opaque CallRequest
// back into a typed llm.Request and calls the real provider. Since this
// sits below paymaster and lookout, it is never called with an unsafe
// request (guardrails have already scanned the messages) and never
// charges a client that's over budget.
type providerCaller struct{ p Provider }

func (c providerCaller) Call(ctx context.Context, req paymaster.CallRequest) (paymaster.CallResponse, error) {
	inReq, ok := req.Inputs.(Request)
	if !ok {
		return paymaster.CallResponse{}, fmt.Errorf("llm/middleware: inputs not llm.Request (got %T)", req.Inputs)
	}
	resp, err := c.p.Complete(ctx, inReq)
	if err != nil {
		return paymaster.CallResponse{}, err
	}
	return paymaster.CallResponse{
		Output:       resp,
		InputTokens:  int64(resp.InputToks),
		OutputTokens: int64(resp.OutputToks),
		CompletedAt:  time.Now().UTC(),
	}, nil
}

// lookoutCaller returns a paymaster.LLMCaller that runs lookout's input
// guard over each user/tool message before letting the request through.
// A Blocked input causes the call to fail with *lookout.BlockedError —
// paymaster above this layer will NOT record a ledger row because we
// never get to next.Call. That's the desired policy: a blocked call is
// not a billable call.
//
// The output guard is NOT wired here because OutputGuard's default
// verdict is sanitize-and-pass, which mutates response text. Doing that
// here would desync the token counts the provider reported from the
// actual text seen by the caller. Output scanning lives in the
// orchestrator streaming pipeline where mutations are visible to the
// agent loop.
func lookoutCaller(next paymaster.LLMCaller, j journal.Emitter) paymaster.LLMCaller {
	if next == nil {
		return nil
	}
	guard := lookout.InputGuard(j)
	return paymaster.CallerFunc(func(ctx context.Context, req paymaster.CallRequest) (paymaster.CallResponse, error) {
		inReq, ok := req.Inputs.(Request)
		if !ok {
			// If we can't unpack the inputs, skip the guard rather than
			// fail the call — the providerCaller will surface the type
			// error with more context.
			return next.Call(ctx, req)
		}
		// Scan every user + tool-result message. System prompts are
		// authored by the platform, not the caller, so they don't need a
		// user-injection guard; ScanInput is tuned for external-origin
		// text anyway.
		for _, m := range inReq.Messages {
			if m.Role != RoleUser && m.Role != RoleTool {
				continue
			}
			if m.Content == "" {
				continue
			}
			if _, err := guard(ctx, m.Content); err != nil {
				return paymaster.CallResponse{}, err
			}
		}
		return next.Call(ctx, req)
	})
}

// paymasterScopeFromContext bridges the lookout Scope (attached by the
// HTTP handler chain) into a paymaster.Scope. The two structs have the
// same fields but aren't aliased — keeping them distinct lets each
// package evolve without dragging the other along. This bridge function
// is the one place that knows they correspond.
//
// If no lookout scope is present in ctx, returns the zero Scope; the
// paymaster will reject the call downstream because WorkspaceID is
// empty. That's the right failure mode — calls without a workspace
// should not be billable.
func paymasterScopeFromContext(ctx context.Context) (paymaster.Scope, bool) {
	ls, ok := lookout.ScopeFromContext(ctx)
	if !ok {
		return paymaster.Scope{}, false
	}
	return paymaster.Scope{
		WorkspaceID: ls.WorkspaceID,
		CrewID:      ls.CrewID,
		AgentID:     ls.AgentID,
		MissionID:   ls.MissionID,
	}, true
}
