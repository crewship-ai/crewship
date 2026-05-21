// MemoryHealthEvaluator (F4.3) runs the daily AGENT.md / PERSONA.md
// / CREW.md hygiene sweep. Inputs come from the existing
// consolidate.ComputeHealth (5-metric score) + a single COUNT
// against memory_relations WHERE relation_kind='refutes' for
// contradiction detection.
//
// Decision space:
//
//	ALLOW    → memory healthy; no action
//	DENY     → auto-trigger consolidation (UI cue + journal entry —
//	           the actual consolidator runs in its existing pipeline)
//	ESCALATE → operator review (mixed signals or contradiction
//	           present — auto-consolidating could destroy evidence)
//
// No new tables — reuses memory_health_snapshots, journal_entries,
// memory_relations exclusively. The decision is persisted as a
// keeper_requests row (request_type='memory_health') so the audit
// trail uses the same surface as the other Phase 2 evaluators.
package gatekeeper

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper"
)

// MemoryHealthEvaluator wires a Gatekeeper bound to the MemoryHealth
// aux slot to the F4.3 decision pipeline.
type MemoryHealthEvaluator struct {
	gk     *Gatekeeper
	logger *slog.Logger
	now    func() time.Time
}

// NewMemoryHealthEvaluator constructs the F4.3 evaluator. The
// provider on gk should be the MemoryHealth aux-slot provider
// (llm.SlotMemoryHealth).
func NewMemoryHealthEvaluator(gk *Gatekeeper, logger *slog.Logger) *MemoryHealthEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &MemoryHealthEvaluator{gk: gk, logger: logger, now: time.Now}
}

// MemoryHealthRequest is the F4.3 input. Snapshot comes from
// consolidate.ComputeHealth; the four byte-size + age fields come
// from a small directory walk in the API handler.
type MemoryHealthRequest struct {
	WorkspaceID        string
	CrewID             string
	AgentName          string
	CrewName           string
	Snapshot           consolidate.HealthSnapshot
	AgentMDBytes       int
	PersonaMDBytes     int
	CrewMDBytes        int
	StalestEntryDays   int
	ContradictionCount int
}

// MemoryHealthResult is the structured outcome. AutoConsolidate is
// true when the handler should kick off a consolidation pass (DENY
// decision); ESCALATE flips it false so the operator can decide.
type MemoryHealthResult struct {
	Decision        keeper.Decision
	Reason          string
	RiskScore       int
	AutoConsolidate bool
	OverallScore    float64
	Prompt          string
	RawLLMResponse  string
}

// Evaluate runs the F4.3 prompt and resolves the decision.
func (e *MemoryHealthEvaluator) Evaluate(ctx context.Context, req MemoryHealthRequest) (MemoryHealthResult, error) {
	if e.gk == nil {
		return MemoryHealthResult{}, fmt.Errorf("memory_health_evaluator: nil gatekeeper")
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		return MemoryHealthResult{}, fmt.Errorf("memory_health_evaluator: empty workspace_id")
	}

	recallRatio := 0.0
	if req.Snapshot.Coverage > 0 {
		// Use Reachability as a recall-vs-write proxy: high reachability
		// + low freshness suggests "memory is being read but not
		// updated"; both low = "stale memory nobody touches".
		recallRatio = req.Snapshot.Reachability / 100.0
	}

	evalReq := EvalRequest{
		Request: keeper.Request{
			ID:                fmt.Sprintf("memhealth_%s_%d", req.CrewID, e.now().UnixNano()),
			RequestingAgentID: req.AgentName,
			RequestingCrewID:  req.CrewID,
			Intent:            "F4.3 daily memory health sweep",
			WorkspaceID:       req.WorkspaceID,
			CreatedAt:         e.now(),
			RequestType:       keeper.RequestTypeMemoryHealth,
		},
		CredentialName: "n/a",
		SecurityLevel:  keeper.SecurityLevelL1,
		AgentName:      req.AgentName,
		CrewName:       req.CrewName,
		RequestType:    keeper.RequestTypeMemoryHealth,
		MemoryHealth: &MemoryHealthInput{
			AgentMDBytes:       req.AgentMDBytes,
			PersonaMDBytes:     req.PersonaMDBytes,
			CrewMDBytes:        req.CrewMDBytes,
			StalestEntryDays:   req.StalestEntryDays,
			RecallToWriteRatio: recallRatio,
			ContradictionCount: req.ContradictionCount,
		},
	}

	resp, err := e.gk.Evaluate(ctx, evalReq)
	if err != nil {
		e.logger.Error("memory_health_evaluator: gatekeeper error → ESCALATE",
			"workspace_id", req.WorkspaceID, "crew_id", req.CrewID, "error", err)
		return MemoryHealthResult{
			Decision:     keeper.DecisionEscalate,
			Reason:       fmt.Sprintf("MemoryHealth LLM error: %v — operator review", err),
			RiskScore:    5,
			OverallScore: req.Snapshot.Overall,
		}, nil
	}

	out := MemoryHealthResult{
		Reason:         resp.Reason,
		RiskScore:      resp.RiskScore,
		OverallScore:   req.Snapshot.Overall,
		Prompt:         resp.Prompt,
		RawLLMResponse: resp.RawLLMResponse,
	}

	// Fail-soft widening: like the other F4 evaluators, surface
	// underlying LLM failures as ESCALATE so we never silently
	// auto-consolidate based on a flaky model. Contradictions are
	// a strong "operator must look" signal — even if the LLM says
	// DENY, ESCALATE wins when ContradictionCount > 0 so the
	// consolidator can't destroy the evidence by collapsing rows.
	if resp.Decision == string(keeper.DecisionDeny) &&
		(isLLMFailureDeny(resp.Reason) || isUnknownDecisionInRaw(resp.RawLLMResponse)) {
		out.Decision = keeper.DecisionEscalate
		out.Reason = "MemoryHealth Curator unavailable or unparseable — operator review (underlying: " + resp.Reason + ")"
		return out, nil
	}

	switch resp.Decision {
	case string(keeper.DecisionAllow):
		out.Decision = keeper.DecisionAllow
	case string(keeper.DecisionDeny):
		if req.ContradictionCount > 0 {
			// Override: contradictions force ESCALATE regardless of
			// what the LLM said. Auto-consolidation would merge or
			// drop the conflicting rows and lose the audit trail the
			// operator needs to decide which side wins.
			out.Decision = keeper.DecisionEscalate
			out.Reason = fmt.Sprintf("DENY overridden — %d contradiction(s) require operator review before consolidation", req.ContradictionCount)
			return out, nil
		}
		out.Decision = keeper.DecisionDeny
		out.AutoConsolidate = true
	case string(keeper.DecisionEscalate):
		out.Decision = keeper.DecisionEscalate
	default:
		out.Decision = keeper.DecisionEscalate
		out.Reason = fmt.Sprintf("Unparseable decision %q — operator review", resp.Decision)
	}
	return out, nil
}
