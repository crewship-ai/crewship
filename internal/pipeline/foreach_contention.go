package pipeline

import (
	"fmt"
	"strings"
)

// ForeachAgentWarning is advisory: it never changes recipe validation.
type ForeachAgentWarning struct {
	Path    string
	Message string
}

// ForeachAgentContentionWarnings identifies fixed agent targets repeated by a
// concurrent foreach. Templated targets cannot be proved equal statically and
// are left unknown. Distinct fixed steps inside one body each repeat across
// items, so even those agents can contend. Other parts of the items can overlap.
func ForeachAgentContentionWarnings(def map[string]any) []ForeachAgentWarning {
	var out []ForeachAgentWarning
	var walkStep func(map[string]any, string, bool)
	var walkSteps func(any, string, bool)
	warn := func(raw any, path string) {
		slug, _ := raw.(string)
		slug = strings.TrimSpace(slug)
		if slug == "" || strings.Contains(slug, "{{") {
			return
		}
		out = append(out, ForeachAgentWarning{Path: path, Message: fmt.Sprintf("%s: agent %q is shared across foreach items; when multiple items run concurrently, its invocations serialize on the agent lock. Other item work may still overlap.", path, slug)})
	}
	walkSteps = func(raw any, path string, parallel bool) {
		steps, _ := raw.([]any)
		for i, rawStep := range steps {
			if step, ok := rawStep.(map[string]any); ok {
				walkStep(step, fmt.Sprintf("%s[%d]", path, i), parallel)
			}
		}
	}
	walkStep = func(step map[string]any, path string, parallel bool) {
		if parallel && step["type"] == "agent_run" {
			warn(step["agent_slug"], path+".agent_slug")
			if outcomes, ok := step["outcomes"].(map[string]any); ok {
				warn(outcomes["grader_agent_slug"], path+".outcomes.grader_agent_slug")
			}
		}
		if step["type"] == "foreach" {
			if fe, ok := step["foreach"].(map[string]any); ok {
				n, _ := fe["parallelism"].(float64)
				walkSteps(fe["steps"], path+".foreach.steps", parallel || n != 1)
			}
		}
		if hooks, ok := step["hooks"].(map[string]any); ok {
			for _, key := range []string{"before", "after"} {
				if hook, ok := hooks[key].(map[string]any); ok {
					walkStep(hook, path+".hooks."+key, parallel)
				}
			}
		}
	}
	walkSteps(def["steps"], "steps", false)
	return out
}
