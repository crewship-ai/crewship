package api

import "github.com/crewship-ai/crewship/internal/orchestrator"

// runSessionProvenance projects everything the run's accumulator captured into
// `metadata` map of a terminal run.* journal payload — the shape
// InternalHandler.UpdateRun writes for the chat and scheduler paths, and the
// one journal.RunAggregated reads back. The run drivers in this package emit
// their terminal entry directly, so without this they answered "which binary,
// which credential, and did it drop an MCP server at startup?" with silence
// while the same run through chat answered it (#1934).
//
// It merges through MergeRunAccumulator rather than MergeSessionInitMeta
// alone. Merging only the provenance half is what made these drivers set
// CaptureResultMeta, capture the resolved model, the cost and the permission
// denials, and then throw all three away — so a delegated run that was
// permission-blocked read as one that chose not to act, and the same work
// through chat did not (#1949).
//
// Returns nil for a nil accumulator (an early dispatch failure that never
// reached the CLI) and for a run whose stream carried no init event, and the
// caller then writes no metadata key at all. The distinction is load-bearing:
// absence reads as "never reported", while an empty cli_version on the record
// reads as "asked and got nothing back" — and the mcp-skip alert can only
// treat a missing mcp_server_errors as "nothing was skipped" if absence keeps
// meaning that.
func runSessionProvenance(acc *orchestrator.Accumulator) map[string]any {
	meta := map[string]any{}
	orchestrator.MergeRunAccumulator(meta, acc, "")
	if len(meta) == 0 {
		return nil
	}
	return meta
}
