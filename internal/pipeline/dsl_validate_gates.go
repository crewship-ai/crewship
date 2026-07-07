package pipeline

import "fmt"

// validateStepGates runs the post-body validation gates for a step:
// complexity tier, on_fail action, and the optional outcomes rubric.
// These are the policy-shape checks that govern how a step's output is
// scored and what happens on failure; they're orthogonal to the body
// shape itself (validateStepEgress / validateStepSlugs) but share the
// per-step loop position.
//
// agentSlugs, when non-nil, gates the outcomes grader_agent_slug
// against the author crew's roster.
func validateStepGates(st Step, agentSlugs map[string]struct{}) error {
	switch st.Complexity {
	case "", ComplexityTrivial, ComplexityFast, ComplexityModerate, ComplexitySmart:
		// ok
	default:
		return fmt.Errorf("pipeline: step %q complexity %q invalid (allowed: trivial|fast|moderate|smart)", st.ID, st.Complexity)
	}

	switch st.OnFail {
	case "", OnFailEscalateTier, OnFailAbort, OnFailRetryStep:
		// ok — retry_step is sugar for the default retry policy
		// (desugared at runStepWithRetry); for the validation/outcomes
		// gate it behaves as escalate_tier.
	default:
		return fmt.Errorf("pipeline: step %q on_fail %q invalid (allowed: escalate_tier|abort|retry_step)", st.ID, st.OnFail)
	}

	if err := validateRetryPolicy(st); err != nil {
		return err
	}

	// Outcomes (rubric-based grading) is only meaningful on
	// agent_run steps — call_pipeline already runs through the
	// nested pipeline's own validation/outcomes. Reject early
	// so authors don't think rubrics will magically apply to
	// nested runs.
	if st.Outcomes != nil {
		if st.Type != StepAgentRun {
			return fmt.Errorf("pipeline: step %q outcomes are only supported on agent_run steps (got %q)", st.ID, st.Type)
		}
		if st.Outcomes.GraderAgentSlug == "" {
			return fmt.Errorf("pipeline: step %q outcomes missing grader_agent_slug", st.ID)
		}
		if !slugRE.MatchString(st.Outcomes.GraderAgentSlug) {
			return fmt.Errorf("pipeline: step %q outcomes.grader_agent_slug %q invalid shape", st.ID, st.Outcomes.GraderAgentSlug)
		}
		if agentSlugs != nil {
			if _, ok := agentSlugs[st.Outcomes.GraderAgentSlug]; !ok {
				return fmt.Errorf("pipeline: step %q outcomes.grader_agent_slug %q not found in author crew", st.ID, st.Outcomes.GraderAgentSlug)
			}
		}
		if len(st.Outcomes.Criteria) == 0 {
			return fmt.Errorf("pipeline: step %q outcomes.criteria empty (rubric needs at least one rule)", st.ID)
		}
		if len(st.Outcomes.Criteria) > 20 {
			return fmt.Errorf("pipeline: step %q outcomes.criteria too long (max 20; got %d) — long rubrics produce noisy grader output", st.ID, len(st.Outcomes.Criteria))
		}
		seenCriteriaNames := make(map[string]struct{}, len(st.Outcomes.Criteria))
		for i, c := range st.Outcomes.Criteria {
			if c.Name == "" {
				return fmt.Errorf("pipeline: step %q outcomes.criteria[%d] missing name", st.ID, i)
			}
			if c.Rule == "" {
				return fmt.Errorf("pipeline: step %q outcomes.criteria[%d] (%q) missing rule", st.ID, i, c.Name)
			}
			if _, dup := seenCriteriaNames[c.Name]; dup {
				return fmt.Errorf("pipeline: step %q outcomes.criteria duplicate name %q", st.ID, c.Name)
			}
			seenCriteriaNames[c.Name] = struct{}{}
		}
		if st.Outcomes.MaxIterations < 0 {
			return fmt.Errorf("pipeline: step %q outcomes.max_iterations cannot be negative", st.ID)
		}
		if st.Outcomes.MaxIterations > 10 {
			return fmt.Errorf("pipeline: step %q outcomes.max_iterations too high (max 10)", st.ID)
		}
		switch st.Outcomes.OnFail {
		case "", OnFailEscalateTier, OnFailAbort, OnFailRetryStep:
			// ok — retry_step normalises to escalate_tier for the rubric gate.
		default:
			return fmt.Errorf("pipeline: step %q outcomes.on_fail %q invalid", st.ID, st.Outcomes.OnFail)
		}
	}
	return nil
}

// validateRetryPolicy rejects a malformed retry: block at authoring time
// — a nonsensical backoff or an uncompilable retry_on predicate should
// fail loudly at save, not silently disable retries at run time.
func validateRetryPolicy(st Step) error {
	rp := st.Retry
	if rp == nil {
		return nil
	}
	if rp.MaxAttempts < 1 {
		return fmt.Errorf("pipeline: step %q retry.max_attempts must be >= 1 (got %d)", st.ID, rp.MaxAttempts)
	}
	if bp := rp.Backoff; bp != nil {
		if bp.MinMs < 0 || bp.MaxMs < 0 {
			return fmt.Errorf("pipeline: step %q retry.backoff delays cannot be negative", st.ID)
		}
		if bp.MaxMs > 0 && bp.MinMs > bp.MaxMs {
			return fmt.Errorf("pipeline: step %q retry.backoff.min_ms (%d) exceeds max_ms (%d)", st.ID, bp.MinMs, bp.MaxMs)
		}
		if bp.Factor != 0 && bp.Factor < 1 {
			return fmt.Errorf("pipeline: step %q retry.backoff.factor must be >= 1 (got %g)", st.ID, bp.Factor)
		}
	}
	if _, err := compileRetryOn(rp.RetryOn); err != nil {
		return fmt.Errorf("pipeline: step %q %w", st.ID, err)
	}
	return nil
}
