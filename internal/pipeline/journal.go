package pipeline

import (
	"context"

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
	workspaceID     string
	authorCrewID    string
	invokingCrewID  string
	invokingAgentID string
	pipelineID      string
	pipelineSlug    string
	runID           string
}

// emitRunStarted records that a pipeline run kicked off. summaryArgs
// land in the entry's Summary; payload carries the full breakdown so
// the Graph view can render run cards without a separate query.
func (c *pipelineEmitContext) emitRunStarted(ctx context.Context, mode RunMode, inputsPreview string, stepCount int) {
	if c == nil {
		return
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
		Payload: map[string]any{
			"pipeline_id":       c.pipelineID,
			"pipeline_slug":     c.pipelineSlug,
			"run_id":            c.runID,
			"mode":              string(mode),
			"author_crew_id":    c.authorCrewID,
			"invoking_crew_id":  c.invokingCrewID,
			"invoking_agent_id": c.invokingAgentID,
			"step_count":        stepCount,
			"inputs_preview":    truncateForPreview(inputsPreview),
		},
	})
}

func (c *pipelineEmitContext) emitStepStarted(ctx context.Context, step Step, stepIndex int, tier AdapterModel) {
	if c == nil {
		return
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " started",
		Payload: map[string]any{
			"pipeline_id":   c.pipelineID,
			"pipeline_slug": c.pipelineSlug,
			"run_id":        c.runID,
			"step_id":       step.ID,
			"step_index":    stepIndex,
			"step_type":     string(step.Type),
			"tier_adapter":  tier.Adapter,
			"tier_model":    tier.Model,
		},
	})
}

func (c *pipelineEmitContext) emitStepCompleted(ctx context.Context, step Step, output string, durationMs int64, costUSD float64) {
	if c == nil {
		return
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepCompleted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " completed",
		Payload: map[string]any{
			"pipeline_id":    c.pipelineID,
			"pipeline_slug":  c.pipelineSlug,
			"run_id":         c.runID,
			"step_id":        step.ID,
			"output_preview": truncateForPreview(output),
			"duration_ms":    durationMs,
			"cost_usd":       costUSD,
		},
	})
}

func (c *pipelineEmitContext) emitStepFailed(ctx context.Context, step Step, errorClass, errorMessage string) {
	if c == nil {
		return
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepFailed,
		Severity:    journal.SeverityError,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " failed",
		Payload: map[string]any{
			"pipeline_id":           c.pipelineID,
			"pipeline_slug":         c.pipelineSlug,
			"run_id":                c.runID,
			"step_id":               step.ID,
			"error_class":           errorClass,
			"error_message_preview": truncateForPreview(errorMessage),
		},
	})
}

func (c *pipelineEmitContext) emitValidationFailed(ctx context.Context, step Step, reason string, action OnFailAction) {
	if c == nil {
		return
	}
	_, _ = c.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: c.workspaceID,
		CrewID:      c.authorCrewID,
		Type:        journal.EntryPipelineStepValidation,
		Severity:    journal.SeverityWarn,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     c.runID,
		Summary:     "Pipeline " + c.pipelineSlug + " step " + step.ID + " validation failed",
		Payload: map[string]any{
			"pipeline_id":   c.pipelineID,
			"pipeline_slug": c.pipelineSlug,
			"run_id":        c.runID,
			"step_id":       step.ID,
			"reason":        reason,
			"action":        string(action),
		},
	})
}

func (c *pipelineEmitContext) emitRunCompleted(ctx context.Context, totalDurationMs int64, totalCostUSD float64) {
	if c == nil {
		return
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
		Payload: map[string]any{
			"pipeline_id":       c.pipelineID,
			"pipeline_slug":     c.pipelineSlug,
			"run_id":            c.runID,
			"total_duration_ms": totalDurationMs,
			"total_cost_usd":    totalCostUSD,
		},
	})
}

func (c *pipelineEmitContext) emitRunFailed(ctx context.Context, failedStepID, errorMessage string) {
	if c == nil {
		return
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
		Payload: map[string]any{
			"pipeline_id":    c.pipelineID,
			"pipeline_slug":  c.pipelineSlug,
			"run_id":         c.runID,
			"failed_at_step": failedStepID,
			"error_message":  truncateForPreview(errorMessage),
		},
	})
}
