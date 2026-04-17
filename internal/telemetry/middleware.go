package telemetry

import (
	"context"

	"github.com/crewship-ai/crewship/internal/paymaster"
)

// LLMMiddleware wraps next in an OpenTelemetry span around every LLM call.
// It matches the paymaster.LLMCaller signature so it composes cleanly with
// paymaster.Middleware and lookout — the full stack looks like:
//
//	telemetry.LLMMiddleware(
//	    paymaster.Middleware(
//	        lookoutMiddleware(
//	            cachingMiddleware(
//	                rawProviderCaller,
//	            ),
//	        ),
//	        j, db,
//	    ),
//	)
//
// Telemetry sits outermost so the span covers the full request lifecycle
// including budget enforcement, guardrails, and cache lookups — an
// operator looking at a "why was this call slow?" trace sees the full
// picture, not just the network hop to the provider.
//
// The span is ended via defer so both success and panic paths clean up.
// Errors from the inner caller mark the span errored and record the
// exception; the error is then returned unchanged so paymaster can still
// record partial billing and callers still get their error back.
func LLMMiddleware(next paymaster.LLMCaller) paymaster.LLMCaller {
	if next == nil {
		return nil
	}
	return paymaster.CallerFunc(func(ctx context.Context, req paymaster.CallRequest) (paymaster.CallResponse, error) {
		ctx, span := StartLLMSpan(ctx, req.Provider, req.Model)
		defer span.End()

		resp, err := next.Call(ctx, req)
		// Usage counts may be zero on error — that's fine, we still want
		// the attributes recorded so the span serialises consistently.
		RecordLLMUsage(span,
			resp.InputTokens,
			resp.OutputTokens,
			resp.CachedInputTokens,
			resp.CacheCreationTokens,
			resp.CostUSD,
		)
		if err != nil {
			RecordError(span, err)
		}
		return resp, err
	})
}
