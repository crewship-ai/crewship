package orchestrator

// MergeRunAccumulator copies everything a finished run's accumulator captured
// into the terminal record's metadata map, and returns the model that actually
// served the run — the resolved one when the CLI reported it, otherwise the
// requested one, which is what a cost ledger needs.
//
// It exists because four drivers assembled this map inline and drifted. The
// chat path merged result usage only on COMPLETED, so a run that FAILED —
// including the no-output failure that is the canonical symptom of a
// permission-blocked run — recorded no permission_denials, the exact case the
// field was added for. The assignment and peer-query paths went further: they
// set CaptureResultMeta, captured cost, model and denials, and then merged only
// the session-init half, throwing the rest away.
//
// None of that was a decision; it was four copies of the same three lines
// falling out of step. One call now decides what a terminal record carries, so
// the next key added applies to every dispatch path and every terminal status
// rather than to whichever copies someone remembered to edit.
//
// Callers keep ownership of their own keys — duration_ms, exit_code, a path's
// own diagnosis like reason=no_output — which are merged before or after as
// they see fit; nothing here overwrites a key it did not write.
//
// acc may be nil: that is the early-dispatch path, where the run failed before
// an agent ever started. Nothing is contributed and the requested model is
// returned unchanged.
func MergeRunAccumulator(dst map[string]any, acc *Accumulator, requestedModel string) string {
	if dst == nil || acc == nil {
		return requestedModel
	}
	MergeResultUsageMeta(dst, acc.ResultMeta())
	MergeSessionInitMeta(dst, acc.SessionInit())
	// The resolved model is ground truth for what the API served; the
	// requested one is only what Crewship asked for, and a subscription can
	// serve a lower tier than the tier that was asked for. Absent means the
	// adapter reported no session-init, so recording the request in its place
	// would answer a question nobody asked.
	if m := acc.ResolvedModel(); m != "" {
		dst["model"] = m
		return m
	}
	return requestedModel
}

// EffectiveModel returns the model that actually served the run — the one the
// CLI reported on its session-init event, or the requested one when it
// reported none.
//
// It is the same answer MergeRunAccumulator returns, split out for callers
// that need the model BEFORE the terminal record is built. The paymaster
// ledger is the one that matters: billing has to happen on every terminal
// path, including the ones that never reach the completed-metadata code, and
// billing a run against a model it did not use is its own kind of wrong.
func EffectiveModel(acc *Accumulator, requestedModel string) string {
	if m := acc.ResolvedModel(); m != "" {
		return m
	}
	return requestedModel
}
