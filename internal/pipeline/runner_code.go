package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// runCodeStep handles a StepCode by delegating to the wired
// CodeRunner. Without a runner, returns a clear "not configured"
// error so the caller knows to wire one — silent no-op would
// produce confusing pipeline behaviour where code blocks "succeed"
// without running.
//
// Inputs from the pipeline's render context flow into the script as
// environment variables. The contract: every declared pipeline
// input becomes CREWSHIP_INPUT_<NAME_UPPER>, plus user-supplied
// step.Code.Env entries pass through verbatim. Step outputs from
// earlier steps don't auto-flow into env (would explode into many
// variables); authors who need them template into a CREWSHIP_INPUT
// via the parent's Inputs map at run time.
//
// Stdout becomes the step's downstream output; stderr is logged in
// the journal but doesn't propagate. ExitCode != 0 → step failure.
func (e *Executor) runCodeStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.Code == nil {
		return "", 0, 0, fmt.Errorf("code step %q missing body", step.ID)
	}
	if e.codeRunner == nil {
		// Production builds do not yet wire a Docker-backed CodeRunner;
		// the interface is in place but the impl is tracked as a
		// separate follow-up. Until that lands, authors should convert
		// `type: code` steps to `type: agent_run` against an agent that
		// has shell-tool access — the agent invokes the same bash from
		// inside its container, which IS already wired end-to-end.
		// Example: see docs/manifest/routine.md "code step workaround".
		return "", 0, 0, fmt.Errorf("code step %q: no CodeRunner wired (production wiring missing) — "+
			"convert this step to type: agent_run with an agent that has shell-tool access, "+
			"or open a follow-up issue tagged 'coderunner-impl'", step.ID)
	}

	// Translate render context inputs → env vars. Use a fresh map
	// so the runner receives only what we promised — never leak
	// arbitrary env from the orchestrator.
	envIn := make(map[string]string, len(parentRender.Inputs)+len(step.Code.Env))
	for k, v := range parentRender.Inputs {
		envIn["CREWSHIP_INPUT_"+strings.ToUpper(k)] = stringify(v)
	}
	for k, v := range step.Code.Env {
		envIn[k] = Render(v, parentRender)
	}

	timeoutSec := step.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 300
	}

	res, err := e.codeRunner.RunCode(ctx, CodeRunRequest{
		WorkspaceID: in.WorkspaceID,
		Runtime:     step.Code.Runtime,
		Version:     step.Code.Version,
		Code:        step.Code.Code,
		InputEnv:    envIn,
		TimeoutSec:  timeoutSec,
		MaxBytes:    1_000_000, // 1 MB stdout cap; matches HTTP step default
	})
	dur := time.Since(stepStart).Milliseconds()
	if err != nil {
		return res.Stdout, 0, dur, fmt.Errorf("code step %q: %w (stderr: %s)", step.ID, err, truncateForGraderLog(res.Stderr))
	}
	if res.ExitCode != 0 {
		return res.Stdout, 0, dur, fmt.Errorf("code step %q exit code %d (stderr: %s)", step.ID, res.ExitCode, truncateForGraderLog(res.Stderr))
	}
	return res.Stdout, 0, dur, nil
}
