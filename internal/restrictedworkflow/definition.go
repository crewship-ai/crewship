// Package restrictedworkflow executes a deliberately bounded typed subset of
// declared routines through isolated, actor-scoped application runners.
package restrictedworkflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

var ErrDenied = errors.New("restricted workflow unavailable")
var ErrUnsupported = errors.New("routine requires unsupported restricted steps or resources")
var refs = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

type definition struct {
	DSL       *pipeline.DSL
	AgentSlug string
}

func compile(raw string) (definition, error) {
	var d definition
	if len(raw) > 128<<10 {
		return d, ErrUnsupported
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil {
		return d, ErrUnsupported
	}
	allowed := map[string]bool{"dsl_version": true, "name": true, "display_name": true, "description": true, "inputs": true, "outputs": true, "steps": true, "slash": true, "estimated_cost_usd": true, "estimated_duration_seconds": true}
	for key := range fields {
		if !allowed[key] {
			return d, ErrUnsupported
		}
	}
	parsed, err := pipeline.Parse([]byte(raw))
	if err != nil || pipeline.Validate(parsed, nil, nil) != nil || len(parsed.Steps) == 0 || len(parsed.Steps) > 16 {
		return d, ErrUnsupported
	}
	inputs := map[string]bool{}
	for _, in := range parsed.Inputs {
		if in.Type != "string" && in.Type != "integer" && in.Type != "number" && in.Type != "boolean" {
			return d, ErrUnsupported
		}
		if in.Format != "" {
			return d, ErrUnsupported
		}
		inputs[in.Name] = true
	}
	previous := map[string]bool{}
	for _, step := range parsed.Steps {
		var sf map[string]json.RawMessage
		if json.Unmarshal(step.Raw, &sf) != nil {
			return d, ErrUnsupported
		}
		for key := range sf {
			if key != "id" && key != "name" && key != "type" && key != "agent_slug" && key != "prompt" && key != "timeout_seconds" {
				return d, ErrUnsupported
			}
		}
		if step.Type != pipeline.StepAgentRun || step.AgentSlug == "" || step.Prompt == "" || len(step.Prompt) > 32768 || step.TimeoutSec < 0 || step.TimeoutSec > 300 {
			return d, ErrUnsupported
		}
		if d.AgentSlug == "" {
			d.AgentSlug = step.AgentSlug
		}
		if step.AgentSlug != d.AgentSlug {
			return d, ErrUnsupported
		}
		for _, match := range refs.FindAllStringSubmatch(step.Prompt, -1) {
			ref := strings.TrimSpace(match[1])
			parts := strings.Split(ref, ".")
			if len(parts) == 2 && parts[0] == "inputs" && inputs[parts[1]] {
				continue
			}
			if len(parts) == 3 && parts[0] == "steps" && previous[parts[1]] && parts[2] == "output" {
				continue
			}
			return d, ErrUnsupported
		}
		previous[step.ID] = true
	}
	d.DSL = parsed
	return d, nil
}
func normalizeInputs(d definition, supplied map[string]any) (map[string]any, error) {
	result := map[string]any{}
	declared := map[string]pipeline.InputSpec{}
	for _, in := range d.DSL.Inputs {
		declared[in.Name] = in
	}
	for key := range supplied {
		if _, ok := declared[key]; !ok {
			return nil, ErrDenied
		}
	}
	for _, in := range d.DSL.Inputs {
		value, ok := supplied[in.Name]
		if !ok && in.Default != nil {
			value, ok = in.Default, true
		}
		if !ok {
			if in.Required {
				return nil, ErrDenied
			}
			continue
		}
		switch in.Type {
		case "string":
			text, valid := value.(string)
			if !valid || len(text) > 16384 {
				return nil, ErrDenied
			}
			if len(in.Options) > 0 && !in.AllowCustom {
				found := false
				for _, option := range in.Options {
					found = found || option == text
				}
				if !found {
					return nil, ErrDenied
				}
			}
		case "boolean":
			if _, valid := value.(bool); !valid {
				return nil, ErrDenied
			}
		case "number", "integer":
			number, valid := value.(float64)
			if !valid || math.IsNaN(number) || math.IsInf(number, 0) || (in.Type == "integer" && math.Trunc(number) != number) || (in.Min != nil && number < *in.Min) || (in.Max != nil && number > *in.Max) {
				return nil, ErrDenied
			}
		default:
			return nil, ErrUnsupported
		}
		result[in.Name] = value
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 16384 {
		return nil, fmt.Errorf("workflow inputs: %w", ErrDenied)
	}
	return result, nil
}
