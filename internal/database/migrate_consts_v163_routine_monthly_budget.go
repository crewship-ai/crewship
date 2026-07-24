package database

// migrationRoutineMonthlyBudget (v163) adds an opt-in MONTHLY spend cap
// per routine (issue #1422 item 3).
//
// This is a distinct concept from DSL.MaxCostUSD (internal/pipeline
// types.go): MaxCostUSD is a hard PER-RUN guardrail authored into the
// routine definition and enforced by the executor mid-run (see
// executor.go's cost-aware retry loop). monthly_budget_usd is a
// workspace-operator-set spend CAP aggregated across every run of the
// routine in the current calendar month — a budget-vs-actual meter, not
// an in-run enforcement gate. The two are independent: a routine can have
// neither, either, or both.
//
// 0 (the default) means "no budget set" — the UI/API distinguish "no cap"
// from "cap of $0" via a separate has-budget signal (pointer at the API
// layer), so a freshly-migrated routine never LOOKS like it's already
// over a zero-dollar budget.
const migrationRoutineMonthlyBudget = `
ALTER TABLE pipelines ADD COLUMN monthly_budget_usd REAL NOT NULL DEFAULT 0;
`
