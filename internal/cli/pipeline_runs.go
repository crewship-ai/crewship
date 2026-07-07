package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PipelineRunDetail mirrors the per-run shape returned by
// GET /api/v1/workspaces/{ws}/pipeline-runs/{runId} — the persisted
// pipeline_runs projection (status, current step, cost, output). Only the
// fields the CLI renders are decoded; unknown members pass through the
// JSON decoder untouched, so server-side additions don't break older CLIs.
type PipelineRunDetail struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	PipelineSlug string  `json:"pipeline_slug"`
	PipelineName string  `json:"pipeline_name"`
	Status       string  `json:"status"`
	Mode         string  `json:"mode"`
	CurrentStep  string  `json:"current_step_id"`
	Output       string  `json:"output"`
	StartedAt    string  `json:"started_at"`
	EndedAt      string  `json:"ended_at"`
	ErrorMessage string  `json:"error_message"`
	FailedAtStep string  `json:"failed_at_step"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMs   int64   `json:"duration_ms"`
	// Inputs + StepOutputs power `routine report` — the run's declared inputs
	// and each step's output (keyed by step_id). Values are decoded loosely
	// (a step output is usually a string but may be structured).
	Inputs      map[string]any `json:"inputs"`
	StepOutputs map[string]any `json:"step_outputs"`
}

// IsTerminal reports whether the pipeline run reached a status that will
// not change further. "waiting" is deliberately NON-terminal — a run
// parked on a human approval resumes when the waitpoint is approved, and
// pollers must keep watching it.
func (r *PipelineRunDetail) IsTerminal() bool {
	switch strings.ToLower(r.Status) {
	case "completed", "failed", "cancelled", "interrupted", "dry_run":
		return true
	}
	return false
}

// GetPipelineRun fetches a single pipeline (routine) run by id, scoped to
// the client's workspace.
func (c *Client) GetPipelineRun(ctx context.Context, id string) (*PipelineRunDetail, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("run id required")
	}
	ws := c.getWorkspaceID(ctx)
	if ws == "" {
		return nil, errors.New("workspace required to look up a routine run")
	}
	resp, err := c.WithContext(ctx).Get(
		"/api/v1/workspaces/" + url.PathEscape(ws) + "/pipeline-runs/" + url.PathEscape(id))
	if err != nil {
		return nil, fmt.Errorf("get routine run %q: %w", id, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("get routine run %q: %w", id, err)
	}
	var detail PipelineRunDetail
	if err := ReadJSON(resp, &detail); err != nil {
		return nil, fmt.Errorf("decode routine run %q: %w", id, err)
	}
	return &detail, nil
}

// PollPipelineRun polls GetPipelineRun(id) at `interval` until the run
// reaches a terminal status, ctx is cancelled, or ctx's deadline passes.
// The shape mirrors PollRun (agent runs) so `crewship wait` can drive
// either kind of run with identical semantics. A nil onTick is allowed;
// when set it fires after every non-terminal read.
func (c *Client) PollPipelineRun(ctx context.Context, id string, interval time.Duration, onTick func(*PipelineRunDetail)) (*PipelineRunDetail, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	// First read happens immediately so already-terminal runs return
	// without waiting a full interval.
	for {
		detail, err := c.GetPipelineRun(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("poll routine run %q: %w", id, err)
		}
		if detail.IsTerminal() {
			return detail, nil
		}
		if onTick != nil {
			onTick(detail)
		}
		select {
		case <-ctx.Done():
			return detail, fmt.Errorf("poll routine run %q: %w", id, ctx.Err())
		case <-t.C:
			continue
		}
	}
}
