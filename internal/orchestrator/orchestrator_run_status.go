package orchestrator

// Status update + output-handler wrappers extracted from
// orchestrator_run.go. Pure file move; signatures and behavior unchanged.

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

// redactionMarker is the prefix every scrubber replacement carries
// ("[REDACTED]" / "[REDACTED:<name>]"). We watch emitted output for it to
// fire the one-shot "scrubber active" system event without re-deriving which
// pattern matched.
const redactionMarker = "[REDACTED"

// wrapScrubHandler returns an output handler that scrubs credential patterns
// from streamed agent output, plus a flush func that MUST be called once the
// stream ends to drain any buffered tail.
//
// SC1 (HIGH, 2026-06 audit): the previous implementation called the stateless
// Scrubber.Scrub per event, so a secret split across two streamed deltas
// ("sk-ant-" then "api03-…") matched no pattern in either chunk and leaked.
// This version drives a stateful scrubber.StreamScrubber that carries an
// overlap buffer across Write calls, so a boundary-straddling secret is
// reassembled and redacted. secretValues (the run's loaded credential plain
// values) are registered for value-aware redaction — catching the literal
// secrets AND their base64/hex/url-encoded/reversed forms even when streamed
// in pieces.
//
// The StreamScrubber wraps a FRESH per-run scrubber.New() (not o.scrubber):
// AddSecretValues mutates the underlying Scrubber by adding patterns, so
// reusing the shared instance would leak one run's credential patterns into
// every other run's output (over-redaction + unbounded growth). Each run gets
// its own isolated base.
//
// Ordering: streamed text/thinking deltas feed the StreamScrubber; any other
// event type (tool_call, tool_result, system, …) first flushes the buffered
// text tail (so output stays in order) and is then scrubbed statelessly before
// forwarding. Call flush() exactly once after the stream drains.
func (o *Orchestrator) wrapScrubHandler(handler EventHandler, secretValues []string) (EventHandler, func()) {
	if handler == nil {
		return handler, func() {}
	}

	base := scrubber.New()
	ss := scrubber.NewStreamScrubber(base)
	if len(secretValues) > 0 {
		ss.AddSecretValues(secretValues...)
	}

	var scrubNotified bool
	notify := func() {
		if scrubNotified {
			return
		}
		scrubNotified = true
		handler(AgentEvent{
			Type:      "system",
			Content:   "[security] Credential pattern detected in agent output -- redacted by stdout scrubber",
			Timestamp: time.Now(),
		})
		o.logger.Warn("scrubber redacted credential in agent output")
	}

	flush := func() {
		tail := ss.Flush()
		if tail == "" {
			return
		}
		if strings.Contains(tail, redactionMarker) {
			notify()
		}
		handler(AgentEvent{Type: "text", Content: tail, Timestamp: time.Now()})
	}

	wrapped := func(event AgentEvent) {
		switch event.Type {
		case "text", "thinking":
			out := ss.Write(event.Content)
			if out == "" {
				// Held entirely in the overlap buffer; nothing safe to emit
				// yet. The content is released by a later Write or flush().
				return
			}
			if strings.Contains(out, redactionMarker) {
				notify()
			}
			event.Content = out
			handler(event)
		default:
			// Preserve ordering: drain any buffered text before this
			// non-text event, then scrub the event's own content with the
			// stateless pass (single, self-contained payloads can't straddle
			// the stream the way text deltas do).
			flush()
			scrubbed := base.Scrub(event.Content)
			if scrubbed != event.Content && strings.Contains(scrubbed, redactionMarker) {
				notify()
			}
			event.Content = scrubbed
			handler(event)
		}
	}

	return wrapped, flush
}

func (o *Orchestrator) updateRunStatus(ctx context.Context, runID, status string) {
	data, err := o.state.Get(ctx, "agent_runs", runID)
	if err != nil {
		o.logger.Error("updateRunStatus: get failed", "run_id", runID, "error", err)
		return
	}
	if data == nil {
		o.logger.Warn("updateRunStatus: run not found", "run_id", runID)
		return
	}
	var run RunState
	if err := json.Unmarshal(data, &run); err != nil {
		o.logger.Error("updateRunStatus: unmarshal failed", "run_id", runID, "error", err)
		return
	}
	run.Status = status
	run.LastActivity = time.Now()
	updated, err := json.Marshal(run)
	if err != nil {
		o.logger.Error("updateRunStatus: marshal failed", "run_id", runID, "error", err)
		return
	}
	if err := o.state.Set(ctx, "agent_runs", runID, updated); err != nil {
		o.logger.Error("updateRunStatus: set failed", "run_id", runID, "error", err)
	}
}
