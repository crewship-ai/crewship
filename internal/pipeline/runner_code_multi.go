package pipeline

import (
	"context"
	"fmt"
)

// MultiCodeRunner is the production CodeRunner: it dispatches a code
// step to the concrete runner for its runtime and rejects any runtime
// with no wired runner (defense-in-depth behind the author-time
// validator in dsl_validate_egress.go). One object is wired via
// SetCodeRunner at boot so adding a runtime is a one-line registry
// edit here + code_runtimes.go, not a change to the wiring.
type MultiCodeRunner struct {
	runners map[string]CodeRunner
}

var _ CodeRunner = (*MultiCodeRunner)(nil)

// NewMultiCodeRunner returns the dispatcher wired with the runtimes
// that have a runner in this build. Keep the keys in lockstep with
// wiredCodeRuntimes (code_runtimes.go).
func NewMultiCodeRunner() *MultiCodeRunner {
	return &MultiCodeRunner{
		runners: map[string]CodeRunner{
			"expr": ExprCodeRunner{},
			"cel":  CelCodeRunner{},
		},
	}
}

func (m *MultiCodeRunner) RunCode(ctx context.Context, req CodeRunRequest) (CodeRunResult, error) {
	r, ok := m.runners[req.Runtime]
	if !ok {
		return CodeRunResult{}, fmt.Errorf(
			"code runtime %q not available in this build (no sandbox wired) — "+
				"use runtime: expr or cel for agentless logic, or convert this step to "+
				"type: agent_run with an agent that has shell-tool access "+
				"(see docs/manifest/routine.md `Code steps`)", req.Runtime)
	}
	return r.RunCode(ctx, req)
}
