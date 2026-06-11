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
		}
	}
	if dsl.Eval != nil && dsl.Eval.Online != nil && dsl.Eval.Online.SampleRate > 0 {
		return fmt.Errorf("pipeline: eval.online sample_rate > 0 not allowed in an agentless routine (grading invokes a grader agent)")
	}
	return nil
}
