package journal

import "testing"

// TestIsFeedRelevant pins the realtime-feed noise gate: high-frequency
// telemetry is dropped, everything a UI rail actually renders is kept, and an
// unknown/new type defaults to relevant (denylist semantics).
func TestIsFeedRelevant(t *testing.T) {
	dropped := []EntryType{
		EntryLLMCacheHit,
		EntryExecOutputChunk,
		EntryContainerMetrics,
		EntryContainerSnapshot,
		EntryRunAgentSpan,
		EntryEvalMetric,
		EntryMemorySearched,
		EntryPipelineStepContainerReady,
		EntryPipelineRunsSwept,
		EntryMemoryVersionsSwept,
	}
	for _, tp := range dropped {
		if IsFeedRelevant(tp) {
			t.Errorf("IsFeedRelevant(%q) = true, want false (telemetry must be dropped)", tp)
		}
	}

	// Types that a journal/run-activity view renders as rows MUST survive the
	// gate — dropping any of these would silently break a live rail.
	kept := []EntryType{
		EntryRunStarted,
		EntryRunCompleted,
		EntryRunFailed,
		EntryAssignmentRun,
		EntryAssignmentDone,
		EntryExecCommand,
		EntryFileWritten,
		EntryNetworkEgress,
		EntryLLMCall,
		EntryKeeperRequest,
		EntryKeeperDecision,
		EntryPipelineRunStarted,
		EntryPipelineStepCompleted,
		EntryMissionStatus,
		// An unrecognised / future type defaults to relevant.
		EntryType("some.future.human_facing_event"),
	}
	for _, tp := range kept {
		if !IsFeedRelevant(tp) {
			t.Errorf("IsFeedRelevant(%q) = false, want true (feed row must be kept)", tp)
		}
	}
}
