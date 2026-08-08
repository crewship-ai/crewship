package pipeline

import "fmt"

// validateAgentless enforces the token-zero guarantee for routines
// that declare `agentless: true`. The allowed step kinds are exactly
// the ones whose runners never touch an LLM: http, code, wait,
// transform.
//
// Rejections and why:
//   - agent_run      — direct LLM spend.
//   - call_pipeline  — the target resolves by slug at RUNTIME; the
//     referenced routine could gain an agent step after this one is
//     saved, silently breaking the guarantee. Statically un-provable
//     in MVP, so rejected outright.
//   - eval.online with sample_rate > 0 — the online sampler runs a
//     grader AGENT against this routine's completed runs, which is
//     token spend attributed to an "agentless" routine.
//
// No-op for agentless=false — existing routines are untouched.
func validateAgentless(dsl *DSL) error {
	if !dsl.Agentless {
		return nil
	}
	for _, st := range dsl.Steps {
		switch st.Type {
		case StepAgentRun:
			return fmt.Errorf("pipeline: step %q is agent_run — not allowed in an agentless routine (token-zero guarantee)", st.ID)
		case StepCallPipeline:
			return fmt.Errorf("pipeline: step %q is call_pipeline — not allowed in an agentless routine (nested target resolves at runtime, guarantee can't be enforced)", st.ID)
		case StepCrewship:
			// Same reasoning as call_pipeline, one hop further out: the verb
			// reaches a handler that MAY start an agent — an @mention in an
			// issue body wakes the mentioned agent (#1768), an assignment
			// dispatches one by definition — and whether it does depends on
			// rendered content, not on anything provable at save time. A
			// token-zero guarantee that holds "usually" is not one.
			return fmt.Errorf("pipeline: step %q is crewship — not allowed in an agentless routine (an issue mention or an assignment can wake an agent, so token-zero can't be enforced)", st.ID)
		case StepForeach:
			// A foreach is agentless only if its whole body is — an
			// agent_run inside the fan-out is token spend all the same.
			if st.Foreach != nil {
				for _, bs := range st.Foreach.Steps {
					if bs.Type == StepAgentRun {
						return fmt.Errorf("pipeline: step %q foreach body contains agent_run %q — not allowed in an agentless routine", st.ID, bs.ID)
					}
				}
			}
		}
	}
	if dsl.Eval != nil && dsl.Eval.Online != nil && dsl.Eval.Online.SampleRate > 0 {
		return fmt.Errorf("pipeline: eval.online sample_rate > 0 not allowed in an agentless routine (grading invokes a grader agent)")
	}
	return nil
}
