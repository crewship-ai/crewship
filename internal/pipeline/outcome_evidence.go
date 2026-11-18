package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// outcomeEvidence is server-owned provenance from accepted agent execution,
// never parsed from a notify/transform result or supplied by authored inputs.
type outcomeEvidence struct {
	Version          int    `json:"version"`
	Executed         bool   `json:"executed"`
	Outcome          string `json:"outcome"`
	SourceRunID      string `json:"source_run_id,omitempty"`
	SourcePipelineID string `json:"source_pipeline_id,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	SourcePath       string `json:"source_path,omitempty"`
}

const unprovenOutcomeReason = "Result lacks accepted outcome evidence; inspect existing outputs before retrying to avoid duplicate effects."

type outcomeCaptureKey struct{}
type outcomeCapture struct{ evidence *outcomeEvidence }

func captureOutcome(ctx context.Context) (context.Context, *outcomeCapture) {
	c := &outcomeCapture{}
	return context.WithValue(ctx, outcomeCaptureKey{}, c), c
}
func recordOutcome(ctx context.Context, e outcomeEvidence) {
	if c, ok := ctx.Value(outcomeCaptureKey{}).(*outcomeCapture); ok {
		c.evidence = &e
	}
}
func acceptedAgentOutcome(ctx context.Context, step Step, output string, runID, pipelineID string) {
	path := executionIn(ctx).path
	if executionIn(ctx).id == "" {
		path += "/" + step.ID
	}
	recordOutcome(ctx, outcomeEvidence{Version: 1, Executed: true, Outcome: orchestrator.ReportedOutcome(output), SourcePath: path, SourceKind: "agent_run", SourceRunID: runID, SourcePipelineID: pipelineID})
}
func (r *RunResult) recordOutcome(step Step, c *outcomeCapture) *outcomeEvidence {
	if !stepsHaveOutcomeCapableStep([]Step{step}) {
		return nil
	}
	evidence := outcomeEvidence{Version: 1, Executed: true, SourcePath: "/" + step.ID, SourceKind: "unproven"}
	if c.evidence != nil {
		evidence = *c.evidence
	}
	if r.outcomeEvidence == nil {
		r.outcomeEvidence = map[string]outcomeEvidence{}
	}
	r.outcomeEvidence[step.ID] = evidence
	return &evidence
}

// Selection follows deterministic execution waves, with DSL-order ties. An
// executed latest agent with no report must not fall back to an earlier success.
func outcomeStepOrder(dsl *DSL) []int {
	order := make([]int, 0, len(dsl.Steps))
	if parallelismMode(dsl) == ParallelismOff || (parallelismMode(dsl) == ParallelismAuto && hasCallPipeline(dsl)) {
		for i := range dsl.Steps {
			order = append(order, i)
		}
		return order
	}
	if parallelismMode(dsl) == ParallelismAuto && !hasCallPipeline(dsl) {
		dsl = deriveAutoNeeds(dsl)
	}
	done := map[string]bool{}
	for len(order) < len(dsl.Steps) {
		var wave []int
		for i, st := range dsl.Steps {
			if done[st.ID] {
				continue
			}
			ready := true
			for _, dep := range st.Needs {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, i)
			}
		}
		if len(wave) == 0 {
			break
		} // technical DAG validation owns cycles/unknown refs
		for _, i := range wave {
			done[dsl.Steps[i].ID] = true
			order = append(order, i)
		}
	}
	return order
}

func selectedOutcome(dsl *DSL, result *RunResult) outcomeEvidence {
	if dsl == nil || result == nil {
		return outcomeEvidence{Version: 1}
	}
	order := outcomeStepOrder(dsl)
	for i := len(order) - 1; i >= 0; i-- {
		st := dsl.Steps[order[i]]
		if !stepsHaveOutcomeCapableStep([]Step{st}) {
			continue
		}
		_, ran := result.StepOutputs[st.ID]
		if !ran {
			continue
		}
		if evidence, ok := result.outcomeEvidence[st.ID]; ok {
			if evidence.Executed {
				return evidence
			}
			continue // an empty foreach or actual agentless child had no reporter
		}
		// Legacy resumed outputs or replacement/sample maps have no accepted
		// provenance. Fail closed instead of trusting a HANDOFF in arbitrary text.
		return outcomeEvidence{Version: 1, Executed: true, SourcePath: "/" + st.ID, SourceKind: "unproven"}
	}
	return outcomeEvidence{Version: 1}
}

// Parallel foreach requires every item's accepted result. Any failed,
// human-dependent, partial or missing report outranks another item's success;
// equal ranks choose the earlier index deterministically. CANCELLED is not a
// trustworthy self-report and is treated as missing, like DeriveOutcome.
func aggregateOutcomes(reports []outcomeEvidence) outcomeEvidence {
	rank := map[string]int{orchestrator.OutcomeFailed: 8, orchestrator.OutcomeNeedsHuman: 7, orchestrator.OutcomePartial: 5, "": 6, orchestrator.OutcomeWorkCreated: 4, orchestrator.OutcomeSucceeded: 3, orchestrator.OutcomeNoChange: 2}
	result := outcomeEvidence{Version: 1}
	best := 0
	for _, e := range reports {
		if !e.Executed {
			continue
		}
		score, ok := rank[e.Outcome]
		if !ok {
			e.Outcome = ""
			score = 6
		}
		if score > best {
			result = e
			best = score
		}
	}
	return result
}

// validOutcomeEvidence admits only complete accepted-agent provenance, an
// explicit unproven/missing report, or the zero report of an agentless step.
func validOutcomeEvidence(e outcomeEvidence) bool {
	if e.Version != 1 {
		return false
	}
	if !e.Executed {
		return e.Outcome == "" && e.SourceKind == "" && e.SourcePath == "" && e.SourceRunID == "" && e.SourcePipelineID == ""
	}
	if e.SourcePath == "" || (e.Outcome != "" && !orchestrator.ValidOutcome(e.Outcome)) {
		return false
	}
	switch e.SourceKind {
	case "agent_run":
		return e.SourceRunID != "" && e.SourcePipelineID != ""
	case "unproven":
		return e.Outcome == "" && e.SourceRunID == "" && e.SourcePipelineID == ""
	default:
		return false
	}
}

func (s *RunStore) getOutcomeEvidence(ctx context.Context, runID string) (map[string]outcomeEvidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT step_id,outcome_evidence_json FROM pipeline_run_step_outputs WHERE run_id=? AND outcome_evidence_json IS NOT NULL`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]outcomeEvidence{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var e outcomeEvidence
		if err := json.Unmarshal([]byte(raw), &e); err != nil || !validOutcomeEvidence(e) {
			return nil, fmt.Errorf("invalid persisted outcome provenance for step %q", id)
		}
		result[id] = e
	}
	return result, rows.Err()
}
