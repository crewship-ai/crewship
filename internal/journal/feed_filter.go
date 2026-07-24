package journal

// highVolumeTelemetryTypes are journal entry types that fire at very high
// frequency — per-token, per-chunk, per-sample, or per-span — and are never
// rendered as individual rows in any human-readable journal feed. They exist
// for storage/analytics, not for a live UI rail.
//
// The journal→WebSocket bridge drops these before fan-out so a busy workspace
// (a mission streaming exec output, an agent making thousands of LLM calls)
// can't turn the realtime channel into a firehose. This is a COARSE,
// bridge-level noise gate, not a substitute for a consumer's own filter: a
// consumer still narrows further (by crew_id / mission_id / entry_type) on the
// REST/SSE paths, which remain the authoritative, gap-free source. Keeping the
// list conservative — only unambiguous telemetry — means a feed row a view
// actually renders (run.*, assignment.*, exec.command, file.written,
// network.egress, llm.call, keeper.*, pipeline.*) is never silently withheld.
var highVolumeTelemetryTypes = map[EntryType]struct{}{
	EntryLLMCacheHit:                {}, // per-cache-hit accounting
	EntryExecOutputChunk:            {}, // streamed stdout/stderr chunks — highest volume
	EntryContainerMetrics:           {}, // periodic resource sampling
	EntryContainerSnapshot:          {}, // periodic container snapshots
	EntryRunAgentSpan:               {}, // per-span tracing telemetry
	EntryEvalMetric:                 {}, // per-metric eval emissions
	EntryMemorySearched:             {}, // per-search retrieval telemetry
	EntryPipelineStepContainerReady: {}, // per-step container-acquire timing
	EntryPipelineRunsSwept:          {}, // housekeeping sweep bookkeeping
	EntryMemoryVersionsSwept:        {}, // housekeeping sweep bookkeeping
}

// IsFeedRelevant reports whether an entry of this type should be forwarded to
// the realtime journal WebSocket channel. It returns false only for
// high-frequency telemetry types (see highVolumeTelemetryTypes); everything
// else — including entries a caller may not recognise — is forwarded, so a
// newly added human-facing entry type reaches the feed without a code change
// here. Denylist by design: the safe default for an unknown type is "show it".
func IsFeedRelevant(t EntryType) bool {
	_, drop := highVolumeTelemetryTypes[t]
	return !drop
}
