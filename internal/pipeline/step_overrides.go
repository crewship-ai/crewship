package pipeline

import (
	"context"
	"database/sql"
)

// StepOverride is a per-step prompt/model patch applied at run start
// over the versioned DSL, so an operator can nudge one step without
// bumping the routine version. Empty fields leave the authored value.
type StepOverride struct {
	Prompt        string
	ModelOverride string
}

// StepOverrideStore reads/writes the routine_step_overrides table (v121).
type StepOverrideStore struct {
	db *sql.DB
}

// NewStepOverrideStore wraps a DB handle.
func NewStepOverrideStore(db *sql.DB) *StepOverrideStore {
	return &StepOverrideStore{db: db}
}

// OverridesFor returns step_id → override for a pipeline. Empty map when
// none — callers treat that as "run the DSL as authored".
func (s *StepOverrideStore) OverridesFor(ctx context.Context, pipelineID string) (map[string]StepOverride, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT step_id, COALESCE(prompt,''), COALESCE(model_override,'') FROM routine_step_overrides WHERE pipeline_id = ?`,
		pipelineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StepOverride{}
	for rows.Next() {
		var stepID, prompt, model string
		if err := rows.Scan(&stepID, &prompt, &model); err != nil {
			return nil, err
		}
		out[stepID] = StepOverride{Prompt: prompt, ModelOverride: model}
	}
	return out, rows.Err()
}

// Set upserts an override for one step. Empty prompt/model clears that
// field (NULL) rather than storing "".
func (s *StepOverrideStore) Set(ctx context.Context, workspaceID, pipelineID, stepID, prompt, model string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO routine_step_overrides (pipeline_id, workspace_id, step_id, prompt, model_override, updated_at)
VALUES (?, ?, ?, ?, ?, datetime('now','subsec'))
ON CONFLICT(pipeline_id, step_id) DO UPDATE SET
    prompt = excluded.prompt,
    model_override = excluded.model_override,
    updated_at = excluded.updated_at`,
		pipelineID, workspaceID, stepID, nullableStr(prompt), nullableStr(model))
	return err
}

// Delete removes a step's override (reverts to the authored value).
func (s *StepOverrideStore) Delete(ctx context.Context, pipelineID, stepID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM routine_step_overrides WHERE pipeline_id = ? AND step_id = ?`,
		pipelineID, stepID)
	return err
}

// applyStepOverrides patches a parsed DSL's steps in place with any
// stored overrides. No-op when the store is nil (tests, builds without
// the override layer) or no overrides exist. Only non-empty override
// fields win, so a prompt-only override leaves the authored model.
func applyStepOverrides(steps []Step, overrides map[string]StepOverride) {
	if len(overrides) == 0 {
		return
	}
	for i := range steps {
		ov, ok := overrides[steps[i].ID]
		if !ok {
			continue
		}
		if ov.Prompt != "" {
			steps[i].Prompt = ov.Prompt
		}
		if ov.ModelOverride != "" {
			// A tier name (trivial|fast|moderate|smart) overrides the
			// step's Complexity — and clears any authored ModelOverride so
			// the tier actually wins (an explicit model id beats the tier
			// at resolution). A non-tier value is treated as a concrete
			// model id and set as ModelOverride.
			if isComplexityTier(ov.ModelOverride) {
				steps[i].Complexity = Complexity(ov.ModelOverride)
				steps[i].ModelOverride = ""
			} else {
				steps[i].ModelOverride = ov.ModelOverride
			}
		}
	}
}

// isComplexityTier reports whether s is one of the named tiers.
func isComplexityTier(s string) bool {
	switch Complexity(s) {
	case ComplexityTrivial, ComplexityFast, ComplexityModerate, ComplexitySmart:
		return true
	}
	return false
}
