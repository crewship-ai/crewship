package pipeline

import "fmt"

// validateStepSlugs runs the step-ID + slug-shape checks for one step.
// Returns nil iff the step's identifier and any slug-typed fields
// (agent_slug for agent_run, pipeline_slug for call_pipeline) are
// well-formed AND, when the caller passed non-nil agentSlugs, the
// referenced agent exists in the author crew.
//
// The seenStepIDs map is mutated on success so the caller's loop
// catches duplicates across iterations.
//
// Returns *ValidationError (not plain error) so the caller can fold it
// straight into the accumulator with a precise JSON-pointer path — this is
// the #1423 item 1 "highest leverage" case: agent_slug is exactly the field
// authors most often typo, so the unknown-agent_slug branch also attaches a
// fuzzy did-you-mean suggestion (internal/fuzzy) against the live roster.
func validateStepSlugs(i int, st Step, dsl *DSL, agentSlugs map[string]struct{}, seenStepIDs map[string]struct{}) *ValidationError {
	base := fmt.Sprintf("/steps/%d", i)
	if st.ID == "" {
		return &ValidationError{Path: base + "/id", Message: fmt.Sprintf("pipeline: step %d missing id", i)}
	}
	if !stepIDRE.MatchString(st.ID) {
		return &ValidationError{Path: base + "/id", Message: fmt.Sprintf("pipeline: step %d id %q invalid", i, st.ID)}
	}
	if _, dup := seenStepIDs[st.ID]; dup {
		return &ValidationError{Path: base + "/id", Message: fmt.Sprintf("pipeline: duplicate step id %q", st.ID)}
	}
	seenStepIDs[st.ID] = struct{}{}

	switch st.Type {
	case StepAgentRun:
		if st.AgentSlug == "" {
			return &ValidationError{Path: base + "/agent_slug", Message: fmt.Sprintf("pipeline: step %q (agent_run) missing agent_slug", st.ID)}
		}
		if !slugRE.MatchString(st.AgentSlug) {
			return &ValidationError{Path: base + "/agent_slug", Message: fmt.Sprintf("pipeline: step %q agent_slug %q invalid shape", st.ID, st.AgentSlug)}
		}
		if st.Prompt == "" {
			return &ValidationError{Path: base + "/prompt", Message: fmt.Sprintf("pipeline: step %q (agent_run) missing prompt", st.ID)}
		}
		if agentSlugs != nil {
			if _, ok := agentSlugs[st.AgentSlug]; !ok {
				msg := fmt.Sprintf("pipeline: step %q references unknown agent_slug %q", st.ID, st.AgentSlug)
				msg += didYouMean(st.AgentSlug, sortedSetKeys(agentSlugs))
				return &ValidationError{Path: base + "/agent_slug", Message: msg}
			}
		}
	case StepCallPipeline:
		if st.PipelineSlug == "" {
			return &ValidationError{Path: base + "/pipeline_slug", Message: fmt.Sprintf("pipeline: step %q (call_pipeline) missing pipeline_slug", st.ID)}
		}
		if !slugRE.MatchString(st.PipelineSlug) {
			return &ValidationError{Path: base + "/pipeline_slug", Message: fmt.Sprintf("pipeline: step %q pipeline_slug %q invalid shape", st.ID, st.PipelineSlug)}
		}
		if st.PipelineSlug == dsl.Name {
			return &ValidationError{Path: base + "/pipeline_slug", Message: fmt.Sprintf("pipeline: step %q calls itself (%q) — direct self-recursion not allowed", st.ID, st.PipelineSlug)}
		}
	}
	return nil
}
