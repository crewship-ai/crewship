package orchestrator

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/logcollector"
)

// Accumulator captures the optional side outputs of a buffering EventHandler:
// the streamed assistant text and the metadata map of the final "result"
// event. Both are read-only via getters so callers can pull them after a run
// without touching the handler's internals.
type Accumulator struct {
	text          strings.Builder
	resultMeta    map[string]any
	resolvedModel string
}

// Text returns the assistant text accumulated from "text" events. It is empty
// unless the handler was built with AccumulateText enabled.
func (a *Accumulator) Text() string {
	if a == nil {
		return ""
	}
	return a.text.String()
}

// ResultMeta returns the metadata map captured from the final "result" event,
// or nil if none was seen (or CaptureResultMeta was disabled).
func (a *Accumulator) ResultMeta() map[string]any {
	if a == nil {
		return nil
	}
	return a.resultMeta
}

// ResolvedModel returns the model id the run ACTUALLY resolved to, captured
// from the CLI's session-init event (ground truth for what the API served vs
// what Crewship asked for via --model). Empty when no init event reported a
// model (non-Claude adapter, or CaptureResultMeta was disabled). Callers
// persist this on the run record so an operator can verify Opus-vs-Sonnet.
func (a *Accumulator) ResolvedModel() string {
	if a == nil {
		return ""
	}
	return a.resolvedModel
}

// BufferingHandlerOpts configures NewBufferingHandler. Every field is optional;
// the zero value yields a handler that only appends to LogBuf (when non-nil).
type BufferingHandlerOpts struct {
	// LogBuf, when non-nil, receives a logcollector.LogEntry for every event.
	// Sites that guard their buffer behind a nil logWriter pass nil here.
	LogBuf *logcollector.OutputBuffer

	// AgentSlug is stamped as the LogEntry.Agent field on every entry.
	AgentSlug string

	// AccumulateText, when true, appends the Content of every "text" event to
	// the returned Accumulator's response builder.
	AccumulateText bool

	// CaptureResultMeta, when true, stores the metadata map of the final
	// "result" event (when it is a map[string]any) on the Accumulator.
	CaptureResultMeta bool

	// OnLogError, when non-nil, is invoked with the error returned by
	// LogBuf.Append. When nil the error is silently ignored, matching sites
	// that drop the buffer write error.
	OnLogError func(error)
}

// NewBufferingHandler builds the EventHandler that every RunAgent call site
// shares: it appends a uniform LogEntry to the output buffer for each event
// and, when enabled, accumulates streamed text and captures the final result
// metadata. The returned Accumulator exposes those captures via Text() and
// ResultMeta().
//
// Per-site extras (WS broadcasts, structured part accumulation, tool
// summaries) are NOT handled here — callers wrap the returned handler and run
// their own logic before or after it, preserving their existing ordering.
func NewBufferingHandler(opts BufferingHandlerOpts) (EventHandler, *Accumulator) {
	acc := &Accumulator{}
	handler := func(event AgentEvent) {
		if opts.AccumulateText && event.Type == "text" {
			acc.text.WriteString(event.Content)
		}
		if opts.CaptureResultMeta && event.Type == "result" {
			if m, ok := event.Metadata.(map[string]any); ok {
				acc.resultMeta = m
			}
		}
		// The session-init system event carries the model the run actually
		// resolved to (see adapter_claude.go). Capture the first one so the
		// run record can record actual-vs-requested model. Gated behind the
		// same flag as result-meta: the sites that finalize a run record
		// (chat bridge, scheduler) are exactly the ones that want it.
		if opts.CaptureResultMeta && event.Type == "system" && acc.resolvedModel == "" {
			if m, ok := event.Metadata.(map[string]any); ok {
				if model, ok := m["model"].(string); ok && model != "" {
					acc.resolvedModel = model
				}
			}
		}
		if opts.LogBuf != nil {
			if err := opts.LogBuf.Append(logcollector.LogEntry{
				Timestamp: event.Timestamp,
				Level:     "info",
				Agent:     opts.AgentSlug,
				Event:     event.Type,
				Content:   event.Content,
				Metadata:  event.Metadata,
			}); err != nil && opts.OnLogError != nil {
				opts.OnLogError(err)
			}
		}
	}
	return handler, acc
}

// ParseResultUsage extracts cost and token usage from a "result" event's
// metadata map. It mirrors the hand-rolled extraction the pipeline runner used:
// total_cost_usd at the top level and input_tokens / output_tokens under a
// nested "usage" map, all expected as JSON float64. Missing fields or wrong
// types yield zero values rather than an error.
func ParseResultUsage(meta any) (costUSD float64, tokIn, tokOut int) {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		return 0, 0, 0
	}
	if v, ok := m["total_cost_usd"].(float64); ok {
		costUSD = v
	}
	if usage, ok := m["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			tokIn = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			tokOut = int(v)
		}
	}
	return costUSD, tokIn, tokOut
}

// resultUsageMetaKeys are the run-summary keys copied verbatim from a "result"
// event's metadata into a run's completed-meta map. Kept as raw values (not
// parsed) so num_turns / model_usage survive untouched.
var resultUsageMetaKeys = []string{"total_cost_usd", "num_turns", "usage", "model_usage"}

// MergeResultUsageMeta copies the standard run-summary usage keys from a
// "result" event's metadata map into dst, leaving any other dst entries (e.g.
// duration_ms) intact. Keys absent from meta are skipped. This dedupes the
// identical key-copy that the chat bridge and scheduler performed inline; it
// deliberately preserves raw values rather than parsing them (use
// ParseResultUsage when you need typed cost/token numbers).
func MergeResultUsageMeta(dst map[string]any, meta any) {
	m, ok := meta.(map[string]any)
	if !ok || m == nil {
		return
	}
	for _, k := range resultUsageMetaKeys {
		if v, ok := m[k]; ok {
			dst[k] = v
		}
	}
}
