// Package quartermaster provides the eval framework and trajectory replay
// surface for Crewship missions. It reads the journal (the append-only
// event log in internal/journal) and derives typed "what happened"
// artifacts from it: a step-by-step Trajectory, aggregate EvalMetrics,
// regression reports that compare two runs, and LLM-as-judge verdicts for
// qualitative scoring.
//
// The package stays provider-neutral: the LLM judge is an interface, so
// callers can plug Ollama, Anthropic, or a stub. Nothing here imports an
// LLM SDK.
//
// "Replay" in this package means observational replay — rehydrate the
// trajectory from the journal and recompute metrics. Re-executing agents
// end-to-end is a later tier (Tier 4) and not in scope here.
package quartermaster

import "time"

// EvalRun is a single evaluation pass over a mission's journal. It is the
// in-memory record returned by Replay; the durable record lives in the
// journal itself as an EntryEvalRunStarted + per-metric EntryEvalMetric
// events. No new DB table is introduced for MVP.
type EvalRun struct {
	ID            string
	MissionID     string
	SeedSignature string // sha256 over step type|tool_name sequence
	StartedAt     time.Time
	CompletedAt   time.Time
	Status        string // "completed", "failed", "partial"
	Metrics       EvalMetrics
	Result        string // free-form summary ("ok", "regression", "inconclusive", etc.)
}

// EvalMetrics is the numeric snapshot computed from a trajectory. These
// are the core TRACE / DeepEval-style signals used for both regression
// detection and LLM judge rubric grounding.
type EvalMetrics struct {
	ToolCallCount    int
	ToolSuccessRate  float64 // 0-1, passed/total across exec.command + keeper.decision outcomes
	StepsToGoal      int
	ConvergenceRatio float64 // optimal/actual (heuristic: pending_tasks_at_start+1 / steps_to_completed)
	TotalCostUSD     float64
	TotalTokens      int64
	Hallucinations   int      // count of guardrail.output_blocked @ warn or error
	FailureModes     []string // MAST taxonomy categories inferred from journal patterns
}

// TrajectoryStep is one meaningful action in a mission, projected from a
// journal entry. Low-value entry types (exec.output_chunk, container.metrics,
// network.port_*) are filtered out by Extract.
type TrajectoryStep struct {
	Index     int
	EntryID   string
	EntryType string
	Summary   string
	ToolName  string // exec.command name, tool called via llm.call, keeper credential, etc.
	Success   bool
	TokenCost int
	ElapsedMs int
}

// MetricDelta is a single compared metric between baseline and candidate.
// Positive Delta means the candidate went up. Regressed flags whether the
// change is bad (direction depends on the metric: cost up = bad, tool
// success rate up = good).
type MetricDelta struct {
	Name      string
	Baseline  float64
	Candidate float64
	Delta     float64
	Regressed bool
	Reason    string // e.g. "tool success dropped 7% (> 5% threshold)"
}

// RegressionReport is the output of Compare. It carries both raw metrics
// for reporting + a Regressed flag the caller can act on (gate a release,
// page on-call, etc.).
type RegressionReport struct {
	Baseline     EvalMetrics
	Candidate    EvalMetrics
	DeltaSummary string
	Regressed    bool
	Deltas       []MetricDelta
}

// JudgeVerdict is the output of a single judge (or an ensemble). Score is
// normalized to 0-1, Confidence similarly. Rubric echoes back the rubric
// items the judge was asked to grade against so callers can audit which
// criterion produced which score. HumanEscalate flags low-confidence
// verdicts that should be routed for manual review.
type JudgeVerdict struct {
	Score         float64
	Confidence    float64
	Reasoning     string
	Rubric        []string
	Scores        []float64 // per-judge scores in an ensemble; single-element for single-judge
	HumanEscalate bool
}
