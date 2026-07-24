package pipeline

import (
	"fmt"
	"strings"
)

// validateForeachStep checks a foreach step's shape (#1419): an items template,
// a non-empty body, a well-formed `as` name, and body steps that are themselves
// valid AND not wait / call_pipeline / nested foreach — the fan-out is a
// bounded, self-contained unit, not a place to park, recurse, or nest another
// fan-out. Body step ids must be unique within the body (their own namespace,
// separate from the enclosing step list).
func validateForeachStep(st Step, dsl *DSL, agentSlugs map[string]struct{}) error {
	if st.Type != StepForeach {
		return nil
	}
	fe := st.Foreach
	if fe == nil {
		return fmt.Errorf("pipeline: step %q (foreach) missing foreach block", st.ID)
	}
	if strings.TrimSpace(fe.Items) == "" {
		return fmt.Errorf("pipeline: step %q (foreach) missing items", st.ID)
	}
	if len(fe.Steps) == 0 {
		return fmt.Errorf("pipeline: step %q (foreach) needs at least one body step", st.ID)
	}
	if fe.As != "" && !stepIDRE.MatchString(fe.As) {
		return fmt.Errorf("pipeline: step %q foreach `as` %q invalid shape", st.ID, fe.As)
	}
	if fe.Parallelism < 0 {
		return fmt.Errorf("pipeline: step %q foreach parallelism must be >= 0", st.ID)
	}
	seen := map[string]struct{}{}
	for i, bs := range fe.Steps {
		switch bs.Type {
		case StepWait:
			return fmt.Errorf("pipeline: step %q foreach body step %q is a wait — not allowed inside foreach", st.ID, bs.ID)
		case StepCallPipeline:
			return fmt.Errorf("pipeline: step %q foreach body step %q is call_pipeline — not allowed inside foreach", st.ID, bs.ID)
		case StepForeach:
			return fmt.Errorf("pipeline: step %q foreach body step %q is a nested foreach — not allowed", st.ID, bs.ID)
		}
		if err := validateStepSlugs(i, bs, dsl, agentSlugs, seen); err != nil {
			return err
		}
		if err := validateStepEgress(bs); err != nil {
			return err
		}
		if err := validateStepCredentials(bs); err != nil {
			return err
		}
	}
	return nil
}
