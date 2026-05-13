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
func validateStepSlugs(i int, st Step, dsl *DSL, agentSlugs map[string]struct{}, seenStepIDs map[string]struct{}) error {
	if st.ID == "" {
		return fmt.Errorf("pipeline: step %d missing id", i)
	}
	if !stepIDRE.MatchString(st.ID) {
		return fmt.Errorf("pipeline: step %d id %q invalid", i, st.ID)
	}
	if _, dup := seenStepIDs[st.ID]; dup {
		return fmt.Errorf("pipeline: duplicate step id %q", st.ID)
	}
	seenStepIDs[st.ID] = struct{}{}

	switch st.Type {
	case StepAgentRun:
		if st.AgentSlug == "" {
			return fmt.Errorf("pipeline: step %q (agent_run) missing agent_slug", st.ID)
		}
		if !slugRE.MatchString(st.AgentSlug) {
			return fmt.Errorf("pipeline: step %q agent_slug %q invalid shape", st.ID, st.AgentSlug)
		}
		if st.Prompt == "" {
			return fmt.Errorf("pipeline: step %q (agent_run) missing prompt", st.ID)
		}
		if agentSlugs != nil {
			if _, ok := agentSlugs[st.AgentSlug]; !ok {
				return fmt.Errorf("pipeline: step %q references unknown agent_slug %q", st.ID, st.AgentSlug)
			}
		}
	case StepCallPipeline:
		if st.PipelineSlug == "" {
			return fmt.Errorf("pipeline: step %q (call_pipeline) missing pipeline_slug", st.ID)
		}
		if !slugRE.MatchString(st.PipelineSlug) {
			return fmt.Errorf("pipeline: step %q pipeline_slug %q invalid shape", st.ID, st.PipelineSlug)
		}
		if st.PipelineSlug == dsl.Name {
			return fmt.Errorf("pipeline: step %q calls itself (%q) — direct self-recursion not allowed", st.ID, st.PipelineSlug)
		}
	}
	return nil
}
