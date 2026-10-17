package harbormaster

import (
	"context"
	"database/sql"
	"errors"
)

// EvaluatorForCrew extends the existing rules only for an explicitly opted-in
// crew. The workspace binding and live crew row are checked on every call;
// callers must not cache this snapshot across a policy change.
func EvaluatorForCrew(ctx context.Context, q DBTX, workspace, crew string, base *Evaluator) (*Evaluator, error) {
	if base == nil {
		base = NewEvaluatorWithDefaults()
	}
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT whole_run_approval FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crew, workspace).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !enabled {
		return base, nil
	}
	rules := append([]RuleMatcher(nil), base.rules...)
	rules = append(rules, RuleMatcher{Name: "crew-whole-run-approval", MapsToKind: KindToolCall, RequireWhen: func(tool string, _ map[string]any) bool { return tool == "agent_run" }})
	return NewEvaluator(rules...), nil
}
