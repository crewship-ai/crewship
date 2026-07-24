package pipeline

// StepQuery runner (#1422 item 4) — a deterministic, read-only aggregate
// query over the run's own workspace's operational data. No LLM, no
// network egress: unlike runHTTPStep this never leaves the process, so it
// carries none of the SSRF/egress-allowlist machinery those steps need.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// maxQueryWindowHours bounds QueryStep.WindowHours regardless of what the
// routine's author configured, so a misconfigured (or malicious) query
// step can't force an unbounded pipeline_runs scan. 720h = 30 days.
const maxQueryWindowHours = 720

// runQueryStep executes the step's configured Source query and returns
// the JSON-marshaled result as the step output — the same convention
// runTransformStep uses for structured output, so a downstream transform
// step can extract a single field (e.g. `.summary_md`) for a notify/http
// step exactly like it would any other step's output.
func (e *Executor) runQueryStep(ctx context.Context, step Step, in RunInput) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.Query == nil {
		return "", 0, 0, fmt.Errorf("query step missing body")
	}
	if e.runStore == nil {
		return "", 0, 0, fmt.Errorf("query step: run history store not configured on this executor")
	}
	switch step.Query.Source {
	case "pipeline_runs":
		windowHours := step.Query.WindowHours
		if windowHours <= 0 {
			windowHours = 24
		}
		if windowHours > maxQueryWindowHours {
			windowHours = maxQueryWindowHours
		}
		stats, err := e.runStore.DigestStats(ctx, in.WorkspaceID, windowHours)
		if err != nil {
			return "", 0, 0, fmt.Errorf("query step: %w", err)
		}
		out, err := json.Marshal(stats)
		if err != nil {
			return "", 0, 0, fmt.Errorf("query step: marshal output: %w", err)
		}
		return string(out), 0, time.Since(stepStart).Milliseconds(), nil
	default:
		// Validation rejects this at save time; belt-and-braces for a
		// definition that smuggled an unsupported source past it.
		return "", 0, 0, fmt.Errorf("query step: unsupported source %q (allowed: pipeline_runs)", step.Query.Source)
	}
}
