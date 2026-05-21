package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Attribute keys — split between the OpenTelemetry GenAI Semantic
// Conventions (gen_ai.*) and Crewship-specific keys (crewship.*).
//
// The gen_ai.* keys must stay verbatim as defined in
// https://opentelemetry.io/docs/specs/semconv/gen-ai/ so any
// OTel-compatible collector picks them up without per-vendor
// mapping. We don't use the typed helpers
// from otel semconv because the GenAI semconv isn't stable in the go
// bindings yet (v1.34 of the module we depend on still marks those keys
// experimental). Hardcoding the string keys here is the official path
// until the helpers land.
//
// The crewship.* keys are free to evolve — they're consumed by our own
// UI and by the journal correlation. Renames must still update the UI
// query and any dashboards built against them, but we aren't constrained
// by a third-party spec.
const (
	// GenAI — system + model. gen_ai.system is the provider family
	// ("anthropic", "openai", "ollama"); gen_ai.request.model is the
	// concrete model slug the caller sent up-stream.
	AttrGenAISystem       = "gen_ai.system"
	AttrGenAIRequestModel = "gen_ai.request.model"

	// GenAI — usage. Counts are post-call, populated via RecordLLMUsage.
	// cached_input_tokens is specifically Anthropic prompt-cache hits; on
	// other providers we leave it zero so dashboards can still compute
	// cache-hit ratios without branching per provider.
	AttrGenAIUsageInputTokens       = "gen_ai.usage.input_tokens"
	AttrGenAIUsageOutputTokens      = "gen_ai.usage.output_tokens"
	AttrGenAIUsageCachedInputTokens = "gen_ai.usage.cached_input_tokens"
	AttrGenAIUsageCacheCreationToks = "gen_ai.usage.cache_creation_tokens"
	AttrGenAICostTotalUSD           = "gen_ai.cost.total_usd"

	// Crewship correlation — allow the trace explorer to group by agent /
	// crew / mission without cross-referencing the journal table.
	AttrCrewshipAgentID   = "crewship.agent.id"
	AttrCrewshipAgentType = "crewship.agent.type"
	AttrCrewshipCrewID    = "crewship.crew.id"
	AttrCrewshipMissionID = "crewship.mission.id"
	AttrCrewshipToolName  = "crewship.tool.name"
	AttrCrewshipToolArgs  = "crewship.tool.args_hash"
	// SideEffect marks tool spans whose execution mutates state outside
	// the agent sandbox (shell, network write, filesystem write). Used by
	// Quartermaster's evaluation replay to decide whether a span is safe
	// to re-run during trajectory replay or must be stubbed.
	AttrCrewshipToolSideEffect = "crewship.tool.side_effect"
)

// StartAgentSpan opens the outermost span for one agent invocation. Everything
// the agent does — tool calls, LLM calls, sub-agent fan-out — happens as
// children of this span. Typical lifetime: the whole `RunAgent` call.
//
// agentID and agentType are REQUIRED; crewID / missionID may be empty when
// the invocation isn't associated with a crew context (e.g. a coordinator
// running a workspace-scoped task). Empty strings are NOT emitted as
// attributes to keep the span payload clean.
func StartAgentSpan(ctx context.Context, agentID, agentType, crewID, missionID string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrCrewshipAgentID, agentID),
	}
	if agentType != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipAgentType, agentType))
	}
	if crewID != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipCrewID, crewID))
	}
	if missionID != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipMissionID, missionID))
	}
	return otel.Tracer(tracerName).Start(ctx, "agent.invoke",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// StartToolSpan wraps a single tool execution. argsHash is the truncated
// SHA-256 of the tool input; we never put raw arguments on the span because
// they may contain secrets (credentials, file contents) that would leak
// through to the collector. The hash lets debuggers match a span back to a
// journal entry that persisted the full args under Keeper protection.
//
// sideEffect should be true for any tool that writes outside the agent
// sandbox (shell, docker, fetch-with-mutation, file writes to shared
// volumes). It informs Quartermaster's replay logic and also gates certain
// cost alerts (idempotent tools can be retried silently, side-effecting
// ones cannot).
func StartToolSpan(ctx context.Context, toolName, argsHash string, sideEffect bool) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String(AttrCrewshipToolName, toolName),
		attribute.Bool(AttrCrewshipToolSideEffect, sideEffect),
	}
	if argsHash != "" {
		attrs = append(attrs, attribute.String(AttrCrewshipToolArgs, argsHash))
	}
	return otel.Tracer(tracerName).Start(ctx, "tool.execute",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// StartLLMSpan wraps one provider call. Token counts and cost are NOT known
// at span start — callers record them after the round-trip via
// RecordLLMUsage. We still set placeholder zero values at Start so the
// attribute is present on the span even if the call errors out before
// recording (so dashboards that aggregate "spans where tokens > 0" don't
// silently miss failed calls).
func StartLLMSpan(ctx context.Context, provider, model string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, "llm.call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(AttrGenAISystem, provider),
			attribute.String(AttrGenAIRequestModel, model),
			attribute.Int64(AttrGenAIUsageInputTokens, 0),
			attribute.Int64(AttrGenAIUsageOutputTokens, 0),
			attribute.Int64(AttrGenAIUsageCachedInputTokens, 0),
			attribute.Int64(AttrGenAIUsageCacheCreationToks, 0),
			attribute.Float64(AttrGenAICostTotalUSD, 0),
		),
	)
}

// RecordLLMUsage stamps post-call usage on the span started by
// StartLLMSpan. Safe to call with a noop span — it's a method on
// trace.Span which always handles the noop path internally.
//
// The attributes overwrite the placeholder zeros set at Start so
// downstream queries don't see both a zero and a real count for the
// same key.
func RecordLLMUsage(span trace.Span, inTok, outTok, cachedIn, cacheCreate int64, costUSD float64) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.Int64(AttrGenAIUsageInputTokens, inTok),
		attribute.Int64(AttrGenAIUsageOutputTokens, outTok),
		attribute.Int64(AttrGenAIUsageCachedInputTokens, cachedIn),
		attribute.Int64(AttrGenAIUsageCacheCreationToks, cacheCreate),
		attribute.Float64(AttrGenAICostTotalUSD, costUSD),
	)
}

// RecordError marks the current span as errored and records the exception
// with OTel's standard semantics. Callers use this in deferred cleanup
// after a provider call so span status is consistent with the returned
// error without every layer having to branch on nil.
func RecordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
