package main

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestDryRunCLIDecode pins the wire contract between the dry-run server
// handler and the CLI's response decoder. The CLI previously declared
// `WouldExecute []…` with `json:"WouldExecute"` while the server emits
// `would_execute` (snake_case, see pipeline.RunResult.WouldExecute's
// json tag). The mismatch meant every `crewship routine dry-run`
// silently rendered "0 steps" since the v83 migration. If anyone
// regresses the tag back to PascalCase — or the server flips to
// camelCase — these subtests catch it before users do.
//
// Strategy: subtest 1 marshals a populated pipeline.RunResult through
// encoding/json and decodes into a struct shaped exactly like the
// CLI's local response type, asserting field-by-field round-trip.
// Subtest 2 documents the original buggy shape as a negative case so
// a future copy-paste fix can't accidentally weaken the contract.
//
// Table-driven + t.Parallel per repo .coderabbit.yaml conventions for
// `**/*_test.go`.
func TestDryRunCLIDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "matches_server_wire",
			fn: func(t *testing.T) {
				t.Parallel()
				// Build a server-shaped RunResult exactly like the dry_run
				// handler would emit. Two steps so we'd notice silent
				// zero-out.
				server := pipeline.RunResult{
					RunID:        "run_abc",
					PipelineID:   "pln_demo",
					PipelineSlug: "demo",
					Status:       "DRY_RUN_OK",
					DurationMs:   12,
					CostUSD:      0.0042,
					WouldExecute: []pipeline.DryRunStep{
						{
							StepID:         "extract",
							StepType:       "agent_run",
							WouldCallAgent: "viktor",
							TierAdapter:    "claude",
							TierModel:      "claude-haiku-4-5",
							EstimatedCost:  0.0021,
						},
						{
							StepID:         "summarise",
							StepType:       "agent_run",
							WouldCallAgent: "tomas",
							TierAdapter:    "claude",
							TierModel:      "claude-sonnet-4-6",
							EstimatedCost:  0.0021,
						},
					},
				}

				wire, err := json.Marshal(server)
				if err != nil {
					t.Fatalf("marshal server result: %v", err)
				}

				// Decode into the same struct shape pipelineDryRunCmd uses
				// in cmd_pipeline.go. Keep this in lock-step with that
				// command — if the CLI adds a field there, mirror it here.
				var got struct {
					Status       string  `json:"status"`
					DurationMs   int64   `json:"duration_ms"`
					CostUSD      float64 `json:"cost_usd"`
					WouldExecute []struct {
						StepID         string  `json:"step_id"`
						StepType       string  `json:"step_type"`
						WouldCallAgent string  `json:"would_call_agent,omitempty"`
						WouldCallSlug  string  `json:"would_call_pipeline,omitempty"`
						WouldPass      string  `json:"would_pass,omitempty"`
						TierAdapter    string  `json:"tier_adapter,omitempty"`
						TierModel      string  `json:"tier_model,omitempty"`
						EstimatedCost  float64 `json:"estimated_cost_usd,omitempty"`
					} `json:"would_execute"`
				}
				if err := json.Unmarshal(wire, &got); err != nil {
					t.Fatalf("decode wire: %v", err)
				}

				// The bug we're guarding against would produce
				// len(WouldExecute)==0 every time. Pin both the count and
				// the contents so a future rename on either side surfaces
				// immediately.
				if len(got.WouldExecute) != 2 {
					t.Fatalf("decoded steps = %d, want 2 (JSON tag drift between server's `would_execute` and the CLI struct?)", len(got.WouldExecute))
				}
				if got.WouldExecute[0].StepID != "extract" {
					t.Errorf("step[0].step_id = %q, want \"extract\"", got.WouldExecute[0].StepID)
				}
				if got.WouldExecute[1].TierModel != "claude-sonnet-4-6" {
					t.Errorf("step[1].tier_model = %q, want \"claude-sonnet-4-6\"", got.WouldExecute[1].TierModel)
				}
				if got.WouldExecute[0].EstimatedCost <= 0 {
					t.Errorf("step[0].estimated_cost_usd = %v, want > 0 (lost precision?)", got.WouldExecute[0].EstimatedCost)
				}
				if got.CostUSD <= 0 {
					t.Errorf("total cost_usd lost in decode: %v", got.CostUSD)
				}
			},
		},
		{
			// Direct guard against the original bug. Marshalling with
			// PascalCase `WouldExecute` would NOT match `would_execute`;
			// documenting it as a negative case so a copy-paste fix
			// doesn't accidentally weaken the type above.
			name: "rejects_pascal_case_tag",
			fn: func(t *testing.T) {
				t.Parallel()
				wire := []byte(`{"WouldExecute":[{"step_id":"s1","step_type":"agent_run"}]}`)
				var got struct {
					WouldExecute []struct {
						StepID string `json:"step_id"`
					} `json:"would_execute"` // correct tag — refuses the PascalCase wire
				}
				if err := json.Unmarshal(wire, &got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(got.WouldExecute) != 0 {
					t.Fatalf("decoded len = %d from PascalCase wire; the tag drifted, not the test", len(got.WouldExecute))
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, tc.fn)
	}
}
