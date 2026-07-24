package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// StepRunResult mirrors POST /workspaces/{ws}/pipelines/{slug}/step_run —
// the verdict of executing ONE step against a fixture, in isolation,
// without a persisted run record. agent_run, http, script, and transform
// steps are supported (#1423 item 3 widened this beyond agent_run only);
// Adapter/Model/TokensIn/TokensOut/CostUSD are agent_run-only and stay
// zero-valued for the deterministic step types. Only the fields the CLI
// renders are decoded; unknown members pass through untouched.
type StepRunResult struct {
	StepID           string   `json:"step_id"`
	StepType         string   `json:"step_type"`
	Adapter          string   `json:"adapter"`
	Model            string   `json:"model"`
	RenderedPrompt   string   `json:"rendered_prompt"`
	Output           string   `json:"output"`
	Valid            bool     `json:"valid"`
	ValidationReason string   `json:"validation_reason"`
	CostUSD          float64  `json:"cost_usd"`
	TokensIn         int      `json:"tokens_in"`
	TokensOut        int      `json:"tokens_out"`
	DurationMs       int64    `json:"duration_ms"`
	Simulated        bool     `json:"simulated"`
	Warnings         []string `json:"warnings"`
}

// StepRunRoutine executes a single step of a routine — agent_run, http,
// script, or transform — against the supplied input fixture (and optional
// seeded upstream step outputs) and returns its output + validation verdict
// + cost (agent_run only; always 0 for the deterministic types), without
// running the rest of the pipeline or writing a run record.
func (c *Client) StepRunRoutine(ctx context.Context, slug, stepID string, inputs map[string]any, stepOutputs map[string]string, tierOverride string) (*StepRunResult, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, errors.New("routine slug required")
	}
	if strings.TrimSpace(stepID) == "" {
		return nil, errors.New("step id required")
	}
	ws := c.getWorkspaceID(ctx)
	if ws == "" {
		return nil, errors.New("workspace required to run a routine step")
	}
	reqBody := map[string]any{"step_id": stepID}
	if len(inputs) > 0 {
		reqBody["inputs"] = inputs
	}
	if len(stepOutputs) > 0 {
		reqBody["step_outputs"] = stepOutputs
	}
	if tierOverride != "" {
		reqBody["tier_override"] = tierOverride
	}
	resp, err := c.WithContext(ctx).Post(
		"/api/v1/workspaces/"+url.PathEscape(ws)+"/pipelines/"+url.PathEscape(slug)+"/step_run", reqBody)
	if err != nil {
		return nil, fmt.Errorf("step-run %q/%q: %w", slug, stepID, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("step-run %q/%q: %w", slug, stepID, err)
	}
	var out StepRunResult
	if err := ReadJSON(resp, &out); err != nil {
		return nil, fmt.Errorf("decode step-run %q/%q: %w", slug, stepID, err)
	}
	return &out, nil
}
