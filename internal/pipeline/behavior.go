package pipeline

import "fmt"

// Behavior describes declared rules using this engine's semantics. It is not
// an audit of a historical run's permissions, tool calls or actual verdicts.
type Behavior struct {
	Steps []StepBehavior `json:"steps"`
	Cost  string         `json:"cost"`
	Scope string         `json:"scope"`
}
type StepBehavior struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Performer string   `json:"performer"`
	Checks    []string `json:"checks"`
	Failure   string   `json:"failure"`
	Attempts  string   `json:"attempts"`
	Timeout   string   `json:"timeout"`
}

// outcomeTierLimit is also used by runAgentStep. Zero preserves legacy tier
// traversal; a positive limit caps it, never creates extra model invocations.
func outcomeTierLimit(step Step, available int) int {
	if step.Outcomes != nil && step.Outcomes.MaxIterations > 0 && step.Outcomes.MaxIterations < available {
		return step.Outcomes.MaxIterations
	}
	return available
}

func DescribeBehavior(dsl *DSL) *Behavior {
	if dsl == nil {
		return nil
	}
	out := &Behavior{Steps: []StepBehavior{}, Cost: "No recipe cost cap declared. Workspace and parent-run budgets may still apply.", Scope: "Recipe rules only. Runtime permissions and actions chosen by agents or called routines are checked separately. These are configured checks, not proof that a run passed them."}
	if dsl.MaxCostUSD > 0 {
		out.Cost = fmt.Sprintf("Recipe cost cap: $%g. Checked at execution boundaries; work already performed cannot be refunded. Stricter parent or workspace limits may apply.", dsl.MaxCostUSD)
	}
	var walk func([]Step, string)
	walk = func(steps []Step, prefix string) {
		for _, step := range steps {
			s := StepBehavior{ID: prefix + step.ID, Name: step.Name, Performer: string(step.Type), Checks: []string{}, Failure: "An execution error fails the run after configured recovery is exhausted.", Attempts: "No step-level execution retry configured. Agent transport recovery and model fallbacks are separate.", Timeout: "No step timeout declared; runner and run limits still apply."}
			if s.Name == "" {
				s.Name = step.ID
			}
			if step.AgentSlug != "" {
				s.Performer = "Agent: " + step.AgentSlug
			}
			if step.Type == StepCallPipeline {
				s.Performer = "Routine: " + step.PipelineSlug
			}
			if step.Type == StepWait && step.Wait != nil {
				s.Performer = "Wait for " + step.Wait.Kind
			}
			if step.TimeoutSec > 0 {
				s.Timeout = fmt.Sprintf("Declared step timeout: %d seconds.", step.TimeoutSec)
			}
			rp := step.Retry
			if rp == nil && step.OnFail == OnFailRetryStep {
				rp = defaultRetryPolicy()
			}
			if rp != nil {
				s.Attempts = fmt.Sprintf("Up to %d step execution attempts, including the first. Cancellation is not retried; model fallbacks and transport recovery are separate.", min(rp.MaxAttempts, retryMaxAttemptsCeiling))
			}
			v := step.Validation
			if v != nil {
				if len(v.Schema) > 0 {
					s.Checks = append(s.Checks, "JSON output schema")
				}
				if v.MinLength != nil {
					s.Checks = append(s.Checks, fmt.Sprintf("Minimum output length: %d bytes", *v.MinLength))
				}
				if v.MaxLength != nil {
					s.Checks = append(s.Checks, fmt.Sprintf("Maximum output length: %d bytes", *v.MaxLength))
				}
				if len(v.MustContain) > 0 {
					s.Checks = append(s.Checks, fmt.Sprintf("%d required text checks", len(v.MustContain)))
				}
				if len(v.MustNotContain) > 0 {
					s.Checks = append(s.Checks, fmt.Sprintf("%d forbidden text checks", len(v.MustNotContain)))
				}
			}
			if len(s.Checks) > 0 && step.Type != StepAgentRun {
				s.Checks = append(s.Checks, "Declared structural checks are not enforced by this live step runner. A fixture test is not a live output gate.")
			}
			if step.Type == StepAgentRun {
				action := step.OnFail
				if action == "" || action == OnFailRetryStep {
					action = OnFailEscalateTier
				}
				if action == OnFailAbort {
					s.Failure = "A structural check failure stops this step."
				} else {
					s.Failure = "A structural check failure tries the next configured model tier with feedback; exhaustion fails the step."
				}
			}
			if o := step.Outcomes; o != nil {
				mode := "Advisory availability: an unavailable checker does not block the result"
				if o.Required {
					mode = "Required: an unavailable checker blocks the result"
				}
				s.Checks = append(s.Checks, fmt.Sprintf("Checker %s: %d criteria. %s.", o.GraderAgentSlug, len(o.Criteria), mode))
				for _, c := range o.Criteria {
					s.Checks = append(s.Checks, c.Name+": "+c.Rule)
				}
				if outcomesOnFail(step) == OnFailAbort {
					s.Failure += " A rejected checker verdict fails the step."
				} else {
					s.Failure += " A rejected checker verdict tries the next configured model tier with feedback."
				}
				if o.MaxIterations > 0 {
					s.Attempts += fmt.Sprintf(" At most %d worker/checker model tiers per execution attempt; stops earlier if no fallback remains.", o.MaxIterations)
				}
			}
			if len(s.Checks) == 0 {
				s.Checks = append(s.Checks, "No output acceptance checks declared. Successful execution alone does not establish result quality.")
			}
			out.Steps = append(out.Steps, s)
			if step.Foreach != nil {
				walk(step.Foreach.Steps, prefix+step.ID+"/")
			}
		}
	}
	walk(dsl.Steps, "")
	return out
}
