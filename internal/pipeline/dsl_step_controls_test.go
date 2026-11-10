package pipeline

import (
	"errors"
	"testing"
)

func TestValidateRejectsUnsupportedStepControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		paths  []string
	}{
		{
			name: "agent step",
			source: `{"name":"controls","steps":[{"id":"classify","type":"agent_run","agent_slug":"worker","prompt":"Classify",
				"permissions":{"memory":"deny"},"max_turns":2}]}`,
			paths: []string{"/steps/0/permissions", "/steps/0/max_turns"},
		},
		{
			name: "foreach child with case variant and null",
			source: `{"name":"controls","steps":[{"id":"batch","type":"foreach","foreach":{"items":"[1]","steps":[
				{"id":"classify","type":"agent_run","agent_slug":"worker","prompt":"Classify","MAX_TURNS":null}]}}]}`,
			paths: []string{"/steps/0/foreach/steps/0/max_turns"},
		},
		{
			name: "step and routine hooks",
			source: `{"name":"controls","hooks":{"before_all":{"type":"http","http":{"method":"GET","url":"https://example.com"},"permissions":{}}},
				"steps":[{"id":"classify","type":"agent_run","agent_slug":"worker","prompt":"Classify",
				"hooks":{"before":{"type":"http","http":{"method":"GET","url":"https://example.com"},"max_turns":0}}}]}`,
			paths: []string{"/steps/0/hooks/before/max_turns", "/hooks/before_all/permissions"},
		},
		{
			name: "unrelated extension remains accepted",
			source: `{"name":"controls","steps":[{"id":"classify","type":"agent_run","agent_slug":"worker","prompt":"Classify",
				"future_extension":{"permissions":{},"max_turns":2}}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsl, err := Parse([]byte(tc.source))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			err = Validate(dsl, nil, nil)
			if len(tc.paths) == 0 {
				if err != nil {
					t.Fatalf("validate extension: %v", err)
				}
				return
			}
			var violations ValidationErrors
			if !errors.As(err, &violations) || len(violations) != len(tc.paths) {
				t.Fatalf("want %v, got %v", tc.paths, err)
			}
			for i, path := range tc.paths {
				if violations[i].Path != path {
					t.Errorf("want path %q, got %v", path, violations[i])
				}
			}
		})
	}
}
