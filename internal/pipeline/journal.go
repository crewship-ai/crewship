package pipeline

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Emitter is the narrow contract the executor needs from a journal
// writer. journal.Writer satisfies this; tests inject a fake.
//
// Keeping the interface in this package (instead of importing the
// concrete *journal.Writer everywhere) means the executor's tests
// don't need to spin up the writer goroutine to verify event
// emission — they pass an in-memory Emitter and inspect the
// captured Entries directly.
type Emitter interface {
	Emit(ctx context.Context, e journal.Entry) (string, error)
}

// WSBroadcaster is the narrow contract the executor needs from the
// WebSocket hub for live pipeline event push. ws.Hub satisfies it;
// tests can pass nil (no broadcast) or a fake. The executor uses
// this to push pipeline.run.* + pipeline.step.* events to clients
// subscribed on the workspace channel, so the Graph view updates
// PipelineRunNode status without polling.
type WSBroadcaster interface {
	BroadcastWorkspace(workspaceID, eventType string, payload any)
}

// nopEmitter swallows all entries. Returned by ensureEmitter when
// the executor is constructed without a journal — useful for
// dry-run-only paths and unit tests that don't care about journal
// emission. Returning a nopEmitter rather than nil-checking on
// every call keeps the executor body terse.
type nopEmitter struct{}

func (nopEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", nil
}

func ensureEmitter(e Emitter) Emitter {
	if e == nil {
		return nopEmitter{}
	}
	return e
}

// previewLen caps the size of output_preview / error_message_preview
// strings written to journal entries. Full output stays in memory
// for the duration of the pipeline run; the journal is for "what
// happened" surface, not for source-of-truth payload storage.
const previewLen = 500

// truncateForPreview returns s if it fits within previewLen, else
// truncates it on a UTF-8 rune boundary and appends a marker so
// consumers know there's more they're not seeing. Slicing on a byte
// boundary would corrupt multi-byte sequences (e.g. cutting a 2-byte
// CJK character in half) and produce invalid UTF-8 in journal
// payloads — JSON encoders downstream replace those with U+FFFD,
// silently losing information.
func truncateForPreview(s string) string {
	if len(s) <= previewLen {
		return s
	}
	cut := previewLen
	// Walk back from the byte boundary until we land on a rune
	// start. UTF-8 continuation bytes have the bit pattern 10xx
	// xxxx; ASCII and rune-start bytes do not. Bound the
	// loop so we never scan more than 4 bytes (max UTF-8 length).
	for cut > 0 && cut > previewLen-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// pipelineEmitContext bundles every value the journal helpers need to
// stamp on every entry. Built once per Executor.Run call and threaded
// through the helpers so each emit site stays a one-liner.
type pipelineEmitContext struct {
	emitter         Emitter
	ws              WSBroadcaster // nil = no live push
	workspaceID     string
	authorCrewID    string
	invokingCrewID  string
	invokingAgentID string
	pipelineID      string
	pipelineSlug    string
	runID           string
}

// broadcast pushes a typed event to the workspace channel. Centralised
// here so every emit site gets WS push for free without each helper
// duplicating the nil check + payload shape. Mirror of journal Emit
// shape so frontend handlers can switch on event type.
func (c *pipelineEmitContext) broadcast(eventType string, payload map[string]any) {
	if c == nil || c.ws == nil || c.workspaceID == "" {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	// Always include the run + pipeline pair so the frontend can
	// route the event to the right PipelineRunNode without parsing
	// nested fields.
	payload["pipeline_id"] = c.pipelineID
	payload["pipeline_slug"] = c.pipelineSlug
	payload["run_id"] = c.runID
	c.ws.BroadcastWorkspace(c.workspaceID, eventType, payload)
}

// emitRunStarted records that a pipeline run kicked off. summaryArgs
// land in the entry's Summary; payload carries the full breakdown so
// the Graph view can render run cards without a separate query.
func (c *pipelineEmitContext) emitRunStarted(ctx context.Context, mode RunMode, inputsPreview string, stepCount int) {
	if c == nil {
		return
	}
	payload := map[string]any{
		"mode":              string(mode),
		"author_crew_id":    c.authorCrewID,
		"invoking_crew_id":  c.invokingCrewID,
		"invoking_agent_id": c.invokingAgentID,
		"step_count":        stepCount,
		"inputs_preview":    truncateForPreview(inputsPreview),
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.invokingCrewID,
		AgentID:     c.invokingAgentID,
		Type:        journal.EntryPipelineRunStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " started",
		Payload:     mergePayload(payload, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.run.started", payload)
}

// emitRunResumed records that a previously in-flight run was
// re-entered at boot from its persisted step state. Reuses
// EntryPipelineRunStarted with a resumed=true marker — a dedicated
// entry type would require a journal migration for what is
// semantically a (re)start; mirrors the emitStepSkipped precedent.
func (c *pipelineEmitContext) emitRunResumed(ctx context.Context, mode RunMode, restoredSteps, stepCount int) {
	if c == nil {
		return
	}
	payload := map[string]any{
		"mode":              string(mode),
		"author_crew_id":    c.authorCrewID,
		"invoking_crew_id":  c.invokingCrewID,
		"invoking_agent_id": c.invokingAgentID,
		"step_count":        stepCount,
		"resumed":           true,
		"restored_steps":    restoredSteps,
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.invokingCrewID,
		AgentID:     c.invokingAgentID,
		Type:        journal.EntryPipelineRunStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " resumed after restart",
		Payload:     mergePayload(payload, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.run.started", payload)
}

// mergePayload returns a new map with the base payload plus the
// supplied key/value pairs. Used to keep journal Entry payload + WS
// broadcast payload alignment without mutating the caller's map.
// Variadic pairs follow the slog/log/value convention: even-indexed
// args are keys (strings), odd-indexed are values.
func mergePayload(base map[string]any, kv ...any) map[string]any {
	// Cap each input length BEFORE the addition so an arithmetic
	// overflow can't reach the make() at all. CodeQL's
	// go/allocation-size-overflow rule flags the bare len() + len()/2
	// expression because the addition itself can wrap on 32-bit ints
	// when one of the inputs is pathologically large — even though
	// the post-add clamp would have rejected the result. min() is on
	// CodeQL's recognised-bound list.
	const maxPayloadHint = 256
	baseLen := min(len(base), maxPayloadHint)
	kvPairs := min(len(kv)/2, maxPayloadHint)
	hint := min(baseLen+kvPairs, maxPayloadHint)
	out := make(map[string]any, hint)
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		out[key] = kv[i+1]
	}
	return out
}

func (c *pipelineEmitContext) emitStepStarted(ctx context.Context, step Step, stepIndex int, tier AdapterModel) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id":      step.ID,
		"step_index":   stepIndex,
		"step_type":    string(step.Type),
		"tier_adapter": tier.Adapter,
		"tier_model":   tier.Model,
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " started",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.step.started", p)
}

func (c *pipelineEmitContext) emitStepCompleted(ctx context.Context, step Step, output string, durationMs int64, costUSD float64) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id":        step.ID,
		"output_preview": truncateForPreview(output),
		"duration_ms":    durationMs,
		"cost_usd":       costUSD,
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepCompleted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " completed",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.step.completed", p)
}

func (c *pipelineEmitContext) emitStepFailed(ctx context.Context, step Step, errorClass, errorMessage string) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id":               step.ID,
		"error_class":           errorClass,
		"error_message_preview": truncateForPreview(errorMessage),
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepFailed,
		Severity:    journal.SeverityError,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " failed",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.step.failed", p)
}

// emitStepSkipped records that a step was skipped because its If
// condition evaluated to false. Distinct from failed (the step
// didn't even attempt execution) and from validation_failed (it
// ran but failed a gate). UI surfaces these as a greyed-out node
// with the condition string in the tooltip.
func (c *pipelineEmitContext) emitStepSkipped(ctx context.Context, step Step, condition string) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id":   step.ID,
		"condition": condition,
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepCompleted, // reuse step.completed type with kind=skipped marker
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " skipped (if=false)",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID, "kind", "skipped"),
	})
	c.broadcast("pipeline.step.skipped", p)
}

// emitStepRetry records a transient failure that the retry policy
// is going to swallow with a sleep. Distinct from emitStepFailed
// (which is the terminal outcome) — the UI shows retry events as
// amber breadcrumbs in the run timeline so observers can see
// "this step needed 2 attempts" without the run going red.
func (c *pipelineEmitContext) emitStepRetry(ctx context.Context, step Step, attempt int, errorMessage string, sleepFor time.Duration) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id":               step.ID,
		"attempt":               attempt,
		"error_message_preview": truncateForPreview(errorMessage),
		"sleep_ms":              sleepFor.Milliseconds(),
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepFailed, // reuse step.failed type w/ attempt counter; dedicated type would require a journal migration
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " retrying (attempt " + intToA(attempt) + ")",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID, "kind", "retry"),
	})
	c.broadcast("pipeline.step.retry", p)
}

func intToA(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func (c *pipelineEmitContext) emitValidationFailed(ctx context.Context, step Step, reason string, action OnFailAction) {
	if c == nil {
		return
	}
	p := map[string]any{
		"step_id": step.ID,
		"reason":  reason,
		"action":  string(action),
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepValidation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " validation failed",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.step.validation_failed", p)
}

func (c *pipelineEmitContext) emitRunCompleted(ctx context.Context, totalDurationMs int64, totalCostUSD float64) {
	if c == nil {
		return
	}
	p := map[string]any{
		"total_duration_ms": totalDurationMs,
		"total_cost_usd":    totalCostUSD,
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.invokingCrewID,
		AgentID:     c.invokingAgentID,
		Type:        journal.EntryPipelineRunCompleted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " completed",
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.run.completed", p)
}

func (c *pipelineEmitContext) emitRunFailed(ctx context.Context, failedStepID, errorMessage string) {
	if c == nil {
		return
	}
	p := map[string]any{
		"failed_at_step": failedStepID,
		"error_message":  truncateForPreview(errorMessage),
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.invokingCrewID,
		AgentID:     c.invokingAgentID,
		Type:        journal.EntryPipelineRunFailed,
		Severity:    journal.SeverityError,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " failed at step " + failedStepID,
		Payload:     mergePayload(p, "pipeline_id", c.pipelineID, "pipeline_slug", c.pipelineSlug, "run_id", c.runID),
	})
	c.broadcast("pipeline.run.failed", p)
}
