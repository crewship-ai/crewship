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
//   - crewship       — one hop further out: the verb reaches a handler
//     that MAY start an agent (an @mention in an issue body wakes the
//     mentioned agent, #1768; an assignment dispatches one by
//     definition), and whether it does depends on rendered content,
//     not on anything provable at save time.
//   - eval.online with sample_rate > 0 — the online sampler runs a
//     grader AGENT against this routine's completed runs, which is
//     token spend attributed to an "agentless" routine.
//
// No-op for agentless=false — existing routines are untouched.
func validateAgentless(dsl *DSL) error {
	if !dsl.Agentless {
		return nil
	}
	if err := agentlessSteps(dsl.Steps, ""); err != nil {
		return err
	}
	if dsl.Eval != nil && dsl.Eval.Online != nil && dsl.Eval.Online.SampleRate > 0 {
		return fmt.Errorf("pipeline: eval.online sample_rate > 0 not allowed in an agentless routine (grading invokes a grader agent)")
	}
	return nil
}

// agentlessSteps applies the rejection list to a step list and RECURSES into
// foreach bodies.
//
// One list, applied everywhere, on purpose. The body scan used to check
// agent_run alone while the top level rejected three kinds — a hole that was
// unreachable only because foreach could not be saved at all (validateStepEgress
// had no case for it and refused every one). The moment foreach became
// saveable, `agentless: true` with a crewship step nested in a foreach body
// saved clean and carried a token-zero guarantee it could not keep: the verb
// can @mention an agent, and the routine bills LLM spend while the UI reports
// it costs nothing.
//
// `where` names the enclosing foreach so the error points at the step a reader
// has to edit rather than at the fan-out.
func agentlessSteps(steps []Step, where string) error {
	in := ""
	if where != "" {
		in = fmt.Sprintf(" (inside foreach %q)", where)
	}
	for _, st := range steps {
		switch st.Type {
		case StepAgentRun:
			return fmt.Errorf("pipeline: step %q%s is agent_run — not allowed in an agentless routine (token-zero guarantee)", st.ID, in)
		case StepCallPipeline:
			return fmt.Errorf("pipeline: step %q%s is call_pipeline — not allowed in an agentless routine (nested target resolves at runtime, guarantee can't be enforced)", st.ID, in)
		case StepCrewship:
			return fmt.Errorf("pipeline: step %q%s is crewship — not allowed in an agentless routine (an issue mention or an assignment can wake an agent, so token-zero can't be enforced)", st.ID, in)
		case StepForeach:
			// A foreach is agentless only if its whole body is — an agent_run
			// inside the fan-out is token spend all the same, and so is a
			// crewship verb or a call_pipeline.
			if st.Foreach != nil {
				if err := agentlessSteps(st.Foreach.Steps, st.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
