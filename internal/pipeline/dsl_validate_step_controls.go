package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Inspect the source fields at the step boundary so unsupported controls
// stay outside the executable Step type and published schema.
type rawStepControls struct {
	Permissions json.RawMessage `json:"permissions"`
	MaxTurns    json.RawMessage `json:"max_turns"`
}

func (c rawStepControls) validate(path string, errs *ValidationErrors) {
	if c.Permissions != nil {
		errs.add(path+"/permissions", errors.New("pipeline: step-level permissions are unsupported; configure the agent's access before running"))
	}
	if c.MaxTurns != nil {
		errs.add(path+"/max_turns", errors.New("pipeline: step-level max_turns is unsupported. Only Claude Code applies the routine turn limit. Use timeout_seconds to bound agent execution time."))
	}
}

type rawStepWithHookControls struct {
	rawStepControls
	Hooks struct {
		Before rawStepControls `json:"before"`
		After  rawStepControls `json:"after"`
	} `json:"hooks"`
}

func (c rawStepWithHookControls) validate(path string, errs *ValidationErrors) {
	c.rawStepControls.validate(path, errs)
	c.Hooks.Before.validate(path+"/hooks/before", errs)
	c.Hooks.After.validate(path+"/hooks/after", errs)
}

func validateStepControls(step *Step, path string, errs *ValidationErrors) {
	if step == nil || len(step.Raw) == 0 {
		return
	}
	var raw struct {
		rawStepWithHookControls
		Foreach struct {
			Steps []rawStepWithHookControls `json:"steps"`
		} `json:"foreach"`
	}
	// Parse already checked the types of the structural fields. The controls
	// themselves may have any value, including null.
	if err := json.Unmarshal(step.Raw, &raw); err != nil {
		return
	}
	raw.rawStepWithHookControls.validate(path, errs)
	if step.Type == StepForeach {
		for i, child := range raw.Foreach.Steps {
			child.validate(fmt.Sprintf("%s/foreach/steps/%d", path, i), errs)
		}
	}
}
