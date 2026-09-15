package pipeline

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Failure kinds — the classes an operator can act on without reading the
// raw engine error. Classification is plain string matching on the engine's
// own formats (runner_*.go, executor*.go, the API run gates), so a wording
// change there is a change here; failure_test.go pins every format.
const (
	FailureCheckerRejected    = "checker_rejected"
	FailureValidationFailed   = "validation_failed"
	FailureTransformInput     = "transform_input"
	FailureTimeout            = "timeout"
	FailureCancelled          = "cancelled"
	FailureMissingCredential  = "missing_credential"
	FailureMissingIntegration = "missing_integration"
	FailureHTTPStatus         = "http_status"
	FailureScriptExit         = "script_exit"
	FailureCostCap            = "cost_cap"
	FailureUnknown            = "unknown"
)

// Failure is the run detail's `failure` member: where a run stopped, why in
// one templated sentence (never model-generated), and which top-level steps
// kept their output versus never ran. The raw error_message stays on the run
// untouched; this is a projection over it.
type Failure struct {
	Kind           string   `json:"kind"`
	StepID         string   `json:"step_id"`
	StepName       string   `json:"step_name"`
	Summary        string   `json:"summary"`
	KeptStepIDs    []string `json:"kept_step_ids"`
	NotDoneStepIDs []string `json:"not_done_step_ids"`
}

var (
	failureHTTPStatusPattern    = regexp.MustCompile(`got http (\d{3})`)
	failureExitCodePattern      = regexp.MustCompile(`exit code (-?\d+)`)
	failureCostExceededPattern  = regexp.MustCompile(`\$([0-9.]+) > \$([0-9.]+)`)
	failureCostRetryCapPattern  = regexp.MustCompile(`breach cap \$([0-9.]+)`)
	failureCredentialPattern    = regexp.MustCompile(`credential of type "([^"]+)"`)
	failureIntegrationPattern   = regexp.MustCompile(`integration "([^"]+)"`)
	failureExpressionPattern    = regexp.MustCompile(`expression "([^"]+)"`)
	failureAfterMarkerMaxLength = 300
)

// ClassifyFailure maps a failed run's error_message onto a Failure. dsl is
// the executed definition (nil when the archive is gone — the kind is still
// classified, the step lists are then empty). stepOutputs are the run's
// recorded top-level outputs; executedStepIDs the step ids with an execution
// record. Both feed kept / not-done; neither is consulted for the kind.
func ClassifyFailure(errorMessage, failedStepID string, dsl *DSL, stepOutputs map[string]string, executedStepIDs []string) *Failure {
	msg := strings.TrimSpace(errorMessage)
	lower := strings.ToLower(msg)
	step := findStep(dsl, failedStepID)
	stepName := failedStepID
	if step != nil && strings.TrimSpace(step.Name) != "" {
		stepName = step.Name
	}
	f := &Failure{
		Kind:           FailureUnknown,
		StepID:         failedStepID,
		StepName:       stepName,
		Summary:        msg,
		KeptStepIDs:    []string{},
		NotDoneStepIDs: []string{},
	}
	stepRef := func() string {
		if stepName == "" {
			return "the failed step"
		}
		return "step " + strconv.Quote(stepName)
	}

	switch {
	case strings.Contains(lower, "cost cap"):
		f.Kind = FailureCostCap
		f.Summary = "The run reached its cost cap."
		if m := failureCostExceededPattern.FindStringSubmatch(msg); m != nil {
			f.Summary = fmt.Sprintf("The run reached its cost cap: $%s spent against a cap of $%s.", m[1], m[2])
		} else if m := failureCostRetryCapPattern.FindStringSubmatch(msg); m != nil {
			f.Summary = fmt.Sprintf("The run stopped before a retry that would have exceeded the cost cap of $%s.", m[1])
		}
	case strings.Contains(lower, "outcomes failed:"):
		f.Kind = FailureCheckerRejected
		feedback := afterLastMarker(msg, "outcomes failed:")
		tiers := ""
		if strings.Contains(lower, "exhausting tiers") {
			tiers = " after every model tier"
			if step != nil && step.Outcomes != nil && step.Outcomes.MaxIterations > 0 {
				tiers = fmt.Sprintf(" after %d model tiers", step.Outcomes.MaxIterations)
			}
		}
		f.Summary = sentence("The checker rejected the result"+tiers, feedback)
	case strings.Contains(lower, "validation failed:") || strings.Contains(lower, "exhausting tiers"):
		f.Kind = FailureValidationFailed
		var reason, tiers string
		if strings.Contains(lower, "validation failed:") {
			reason = afterLastMarker(msg, "validation failed:")
		} else {
			reason = afterLastMarker(msg, "exhausting tiers:")
			tiers = " after every model tier"
		}
		f.Summary = sentence("The output failed a structural check"+tiers, reason)
	case strings.Contains(lower, "input is not json"):
		f.Kind = FailureTransformInput
		f.Summary = fmt.Sprintf("%s received input that is not JSON.", upperFirst(stepRef()))
		if m := failureExpressionPattern.FindStringSubmatch(msg); m != nil {
			f.Summary = fmt.Sprintf("%s received input that is not JSON; the expression %q needs JSON.", upperFirst(stepRef()), m[1])
		}
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded"):
		f.Kind = FailureTimeout
		switch {
		case strings.Contains(lower, "(approval) timed out"):
			f.Summary = fmt.Sprintf("Nobody answered the approval at %s before its deadline.", stepRef())
		case step != nil && step.TimeoutSec > 0:
			f.Summary = fmt.Sprintf("%s did not finish within its %d-second limit.", upperFirst(stepRef()), step.TimeoutSec)
		default:
			f.Summary = fmt.Sprintf("%s did not finish within its time limit.", upperFirst(stepRef()))
		}
	case strings.Contains(lower, "cancel"):
		f.Kind = FailureCancelled
		if stepName == "" {
			f.Summary = "The run was stopped before it finished."
		} else {
			f.Summary = fmt.Sprintf("The run was stopped at %s.", stepRef())
		}
	case strings.Contains(lower, "credential") &&
		(strings.Contains(lower, "not present in the vault") || strings.Contains(lower, "no active") || strings.Contains(lower, "no credential")):
		f.Kind = FailureMissingCredential
		f.Summary = "A required credential is missing from the vault."
		if m := failureCredentialPattern.FindStringSubmatch(msg); m != nil {
			f.Summary = fmt.Sprintf("A required credential of type %q is missing from the vault.", m[1])
		}
	case strings.Contains(lower, "integration") && strings.Contains(lower, "not connected"):
		f.Kind = FailureMissingIntegration
		f.Summary = "A required integration is not connected for the author crew."
		if m := failureIntegrationPattern.FindStringSubmatch(msg); m != nil {
			f.Summary = fmt.Sprintf("The integration %q is not connected for the author crew.", m[1])
		}
	case failureHTTPStatusPattern.MatchString(lower):
		f.Kind = FailureHTTPStatus
		m := failureHTTPStatusPattern.FindStringSubmatch(lower)
		f.Summary = fmt.Sprintf("The HTTP call in %s returned status %s.", stepRef(), m[1])
	case failureExitCodePattern.MatchString(lower):
		f.Kind = FailureScriptExit
		m := failureExitCodePattern.FindStringSubmatch(lower)
		if step != nil && step.Script != nil && strings.TrimSpace(step.Script.Path) != "" {
			f.Summary = fmt.Sprintf("The script %s in %s exited with code %s.", strings.TrimSpace(step.Script.Path), stepRef(), m[1])
		} else {
			f.Summary = fmt.Sprintf("The script in %s exited with code %s.", stepRef(), m[1])
		}
	default:
		if f.Summary == "" {
			f.Summary = "The run did not finish."
		}
	}

	if dsl != nil {
		executed := map[string]bool{}
		for _, id := range executedStepIDs {
			executed[id] = true
		}
		failedIdx := -1
		for i := range dsl.Steps {
			if dsl.Steps[i].ID == failedStepID {
				failedIdx = i
				break
			}
		}
		for i := range dsl.Steps {
			id := dsl.Steps[i].ID
			if _, ok := stepOutputs[id]; ok {
				f.KeptStepIDs = append(f.KeptStepIDs, id)
				continue
			}
			if i > failedIdx && !executed[id] {
				f.NotDoneStepIDs = append(f.NotDoneStepIDs, id)
			}
		}
	}
	return f
}

// findStep locates a step by id anywhere in the recipe — top level, foreach
// bodies, step hooks and routine hooks — so a failure inside a body or hook
// still gets its declared name.
func findStep(dsl *DSL, id string) *Step {
	if dsl == nil || id == "" {
		return nil
	}
	var found *Step
	var visit func(s *Step)
	var walk func(steps []Step)
	visit = func(s *Step) {
		if s == nil || found != nil {
			return
		}
		if s.ID == id {
			found = s
			return
		}
		if s.Hooks != nil {
			visit(s.Hooks.Before)
			visit(s.Hooks.After)
		}
		if s.Foreach != nil {
			walk(s.Foreach.Steps)
		}
	}
	walk = func(steps []Step) {
		for i := range steps {
			if found != nil {
				return
			}
			visit(&steps[i])
		}
	}
	walk(dsl.Steps)
	if dsl.Hooks != nil {
		visit(dsl.Hooks.BeforeAll)
		visit(dsl.Hooks.AfterAll)
		visit(dsl.Hooks.OnFailure)
	}
	return found
}

// afterLastMarker returns the text after the last (case-insensitive)
// occurrence of marker, trimmed and bounded — the engine nests reasons
// ("… exhausting tiers: outcomes failed: outcomes failed: <feedback>"), and
// the innermost one is the feedback a person wants.
func afterLastMarker(msg, marker string) string {
	idx := strings.LastIndex(strings.ToLower(msg), strings.ToLower(marker))
	if idx < 0 {
		return strings.TrimSpace(msg)
	}
	rest := strings.TrimSpace(msg[idx+len(marker):])
	return truncateRunes(rest, failureAfterMarkerMaxLength)
}

// sentence joins a lead-in and a detail as "Lead-in: detail." — with the
// terminal period added only when the detail does not already end one.
func sentence(lead, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return lead + "."
	}
	if strings.HasSuffix(detail, ".") || strings.HasSuffix(detail, "!") || strings.HasSuffix(detail, "?") {
		return lead + ": " + detail
	}
	return lead + ": " + detail + "."
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
