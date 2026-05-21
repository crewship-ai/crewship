// Package routines holds the daily-cadence sweeps PRD §6 F4 schedules
// against the Keeper Phase 2 evaluators.
//
// Two RoutineKind functions ship here today:
//
//	RunSkillReview        — sweeps every skill, evaluates via F4.1
//	RunMemoryHealthCheck  — sweeps every (workspace, crew), evaluates via F4.3
//
// Both are *pure-ish* in the same sense as the evaluators: they take
// a context + dependencies + return a summary. The actual cron
// registration lives in the scheduler bootstrap (a follow-up wire-up
// commit — these routines first must be import-stable so they can be
// referenced from the scheduler config).
//
// Mirror of the PR-E peer_card_routine.go pattern: the routine is
// the orchestration glue (load candidates, iterate, persist summary);
// the evaluator is the per-item LLM decision. This split keeps the
// LLM-stubbing tests focused on the evaluator and the
// SQL/sweep-coverage tests focused on the routine.
package routines

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/skills"
)

// RoutineKind is the typed selector the scheduler will switch on to
// pick the right Run function. Closed set — adding a kind requires
// adding the function + extending the scheduler bootstrap.
type RoutineKind string

const (
	// RoutineKindSkillReview wraps RunSkillReview (PRD §6 F4.1).
	// Daily cadence by default; configurable via the same cron-spec
	// surface the scheduler reads for agent runs.
	RoutineKindSkillReview RoutineKind = "skill_review"
	// RoutineKindMemoryHealthCheck wraps RunMemoryHealthCheck (PRD §6 F4.3).
	// Daily cadence by default; same configuration surface as above.
	RoutineKindMemoryHealthCheck RoutineKind = "memory_health_check"
)

// SkillReviewSummary tracks per-outcome counters for a single sweep.
// Persisted in the routine_runs table via the scheduler bootstrap;
// also surfaced in the journal entry the routine emits on completion.
type SkillReviewSummary struct {
	ScannedSkills    int
	VerifiedAllowed  int // ALLOW with VerifyAfterDecide
	UnverifiedDenied int // DENY with UnverifyAfterDecide
	EscalatedToInbox int // ESCALATE
	LifecycleFlipped int // skills whose lifecycle_state was updated
	Errors           int // per-skill evaluator errors (sweep continues)
}

// MemoryHealthSummary mirrors SkillReviewSummary for F4.3.
type MemoryHealthSummary struct {
	ScannedScopes    int // (workspace, crew) pairs visited
	HealthyAllowed   int
	AutoConsolidated int
	EscalatedToInbox int
	Errors           int
}

// SkillRow is the minimal columns the F4.1 sweep needs per skill. The
// routine_run handler does the SELECT; the package stays SQL-agnostic
// so the unit tests can hand-feed rows.
type SkillRow struct {
	ID             string
	Name           string
	Description    string
	WorkspaceID    string
	Slug           string
	LifecycleState skills.LifecycleState
	LastUsedAt     time.Time
	Assignments    int
}

// SkillSweepInput bundles the per-skill snapshot the evaluator needs
// alongside the post-decision side effects (verification flip, lifecycle
// update). The routine pulls these from skills + agent_skills +
// skill_invocations; the package contract requires the caller pre-load
// them rather than doing SQL inline.
type SkillSweepInput struct {
	Skill          SkillRow
	AssignedAgents []string
	Stats          gatekeeper.SkillStats
	FailurePeeks   []string
}

// SkillSweepPersister is the side-effect interface the F4.1 sweep
// calls into for each non-no-op decision. The scheduler bootstrap
// supplies a SQL implementation; tests use a recording fake.
type SkillSweepPersister interface {
	// MarkVerified updates skills.verification='VERIFIED' (idempotent).
	MarkVerified(ctx context.Context, skillID string) error
	// MarkUnverified updates skills.verification='UNVERIFIED' (idempotent).
	MarkUnverified(ctx context.Context, skillID string) error
	// SetLifecycle updates skills.lifecycle_state if next != current.
	SetLifecycle(ctx context.Context, skillID string, next skills.LifecycleState, reason string) error
	// WriteInboxItem persists an ESCALATE decision as an inbox row.
	WriteInboxItem(ctx context.Context, skillID, reason string, blocking bool) error
}

// RunSkillReview iterates the supplied skills, evaluates each via the
// F4.1 evaluator, and routes the decision through the persister. The
// sweep is best-effort: a per-skill error is logged + counted in
// Summary.Errors but does NOT abort the sweep. PRD §6 F4.1 explicitly
// calls for "audit, not gate" semantics — better to finish the sweep
// with some skills unevaluated than to leave half the catalog stale.
func RunSkillReview(
	ctx context.Context,
	ev *gatekeeper.SkillReviewEvaluator,
	persister SkillSweepPersister,
	inputs []SkillSweepInput,
	logger *slog.Logger,
) (SkillReviewSummary, error) {
	if ev == nil {
		return SkillReviewSummary{}, errors.New("routines: nil evaluator")
	}
	if persister == nil {
		return SkillReviewSummary{}, errors.New("routines: nil persister")
	}
	if logger == nil {
		logger = slog.Default()
	}

	now := time.Now().UTC()
	var sum SkillReviewSummary
	for _, in := range inputs {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sum, ctxErr
		}
		sum.ScannedSkills++

		snap := skills.LifecycleSnapshot{
			Current:           in.Skill.LifecycleState,
			LastUsedAt:        in.Skill.LastUsedAt,
			ActiveAssignments: in.Skill.Assignments,
			Now:               now,
		}
		req := gatekeeper.SkillReviewRequest{
			SkillID:          in.Skill.ID,
			SkillName:        in.Skill.Name,
			SkillDescription: in.Skill.Description,
			WorkspaceID:      in.Skill.WorkspaceID,
			AgentName:        "system",
			CrewName:         "system",
			LifecycleSnap:    snap,
			AssignedAgents:   in.AssignedAgents,
			Stats:            in.Stats,
			FailureSnippets:  in.FailurePeeks,
		}
		res, err := ev.Evaluate(ctx, req)
		if err != nil {
			sum.Errors++
			logger.Error("routines.skill_review: evaluator error",
				"skill_id", in.Skill.ID, "error", err)
			continue
		}

		// Verification side effects.
		switch {
		case res.VerifyAfterDecide:
			sum.VerifiedAllowed++
			if perr := persister.MarkVerified(ctx, in.Skill.ID); perr != nil {
				sum.Errors++
				logger.Error("routines.skill_review: MarkVerified",
					"skill_id", in.Skill.ID, "error", perr)
			}
		case res.UnverifyAfterDecide:
			sum.UnverifiedDenied++
			if perr := persister.MarkUnverified(ctx, in.Skill.ID); perr != nil {
				sum.Errors++
				logger.Error("routines.skill_review: MarkUnverified",
					"skill_id", in.Skill.ID, "error", perr)
			}
		}

		// Lifecycle side effect: write only when the transition flips.
		if res.ProposedLifecycle.Next != "" &&
			res.ProposedLifecycle.Next != in.Skill.LifecycleState {
			sum.LifecycleFlipped++
			if perr := persister.SetLifecycle(ctx, in.Skill.ID, res.ProposedLifecycle.Next, res.ProposedLifecycle.Reason); perr != nil {
				sum.Errors++
				logger.Error("routines.skill_review: SetLifecycle",
					"skill_id", in.Skill.ID, "next", res.ProposedLifecycle.Next, "error", perr)
			}
		}

		// Inbox side effect for ESCALATE / DENY (the operator should see).
		if res.Decision == keeper.DecisionEscalate {
			sum.EscalatedToInbox++
			if perr := persister.WriteInboxItem(ctx, in.Skill.ID, res.Reason, false); perr != nil {
				sum.Errors++
				logger.Error("routines.skill_review: WriteInboxItem",
					"skill_id", in.Skill.ID, "error", perr)
			}
		} else if res.Decision == keeper.DecisionDeny {
			// DENY blocks the operator's UI: surface as a *blocking* inbox
			// item so the unverify isn't quietly lost in the noise.
			if perr := persister.WriteInboxItem(ctx, in.Skill.ID, res.Reason, true); perr != nil {
				sum.Errors++
				logger.Error("routines.skill_review: WriteInboxItem (deny)",
					"skill_id", in.Skill.ID, "error", perr)
			}
		}
	}

	logger.Info("routines.skill_review: sweep complete",
		"scanned", sum.ScannedSkills,
		"verified", sum.VerifiedAllowed,
		"unverified", sum.UnverifiedDenied,
		"escalated", sum.EscalatedToInbox,
		"lifecycle_flipped", sum.LifecycleFlipped,
		"errors", sum.Errors)
	return sum, nil
}

// MemoryHealthScope is the per-scope input for the F4.3 sweep. The
// scheduler bootstrap loads one of these per (workspace, crew) pair
// — typically by SELECT FROM crews — and feeds them to RunMemoryHealthCheck.
type MemoryHealthScope struct {
	WorkspaceID        string
	CrewID             string
	CrewName           string
	Snapshot           consolidate.HealthSnapshot
	AgentMDBytes       int
	PersonaMDBytes     int
	CrewMDBytes        int
	StalestEntryDays   int
	ContradictionCount int
}

// MemoryHealthPersister is the side-effect interface for F4.3.
type MemoryHealthPersister interface {
	// TriggerConsolidation kicks off the consolidator pipeline for the
	// scope. Implementations may queue an async job; the routine does
	// not wait for completion.
	TriggerConsolidation(ctx context.Context, workspaceID, crewID, reason string) error
	// WriteInboxItem persists an ESCALATE decision as an inbox row.
	WriteInboxItem(ctx context.Context, workspaceID, crewID, reason string, blocking bool) error
}

// RunMemoryHealthCheck iterates the supplied scopes, evaluates each
// via the F4.3 evaluator, and routes the decision. Best-effort sweep,
// same semantics as RunSkillReview.
func RunMemoryHealthCheck(
	ctx context.Context,
	ev *gatekeeper.MemoryHealthEvaluator,
	persister MemoryHealthPersister,
	scopes []MemoryHealthScope,
	logger *slog.Logger,
) (MemoryHealthSummary, error) {
	if ev == nil {
		return MemoryHealthSummary{}, errors.New("routines: nil evaluator")
	}
	if persister == nil {
		return MemoryHealthSummary{}, errors.New("routines: nil persister")
	}
	if logger == nil {
		logger = slog.Default()
	}

	var sum MemoryHealthSummary
	for _, sc := range scopes {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return sum, ctxErr
		}
		sum.ScannedScopes++

		req := gatekeeper.MemoryHealthRequest{
			WorkspaceID:        sc.WorkspaceID,
			CrewID:             sc.CrewID,
			AgentName:          "system",
			CrewName:           sc.CrewName,
			Snapshot:           sc.Snapshot,
			AgentMDBytes:       sc.AgentMDBytes,
			PersonaMDBytes:     sc.PersonaMDBytes,
			CrewMDBytes:        sc.CrewMDBytes,
			StalestEntryDays:   sc.StalestEntryDays,
			ContradictionCount: sc.ContradictionCount,
		}
		res, err := ev.Evaluate(ctx, req)
		if err != nil {
			sum.Errors++
			logger.Error("routines.memory_health: evaluator error",
				"workspace_id", sc.WorkspaceID, "crew_id", sc.CrewID, "error", err)
			continue
		}

		switch res.Decision {
		case keeper.DecisionAllow:
			sum.HealthyAllowed++
		case keeper.DecisionDeny:
			if res.AutoConsolidate {
				sum.AutoConsolidated++
				if perr := persister.TriggerConsolidation(ctx, sc.WorkspaceID, sc.CrewID, res.Reason); perr != nil {
					sum.Errors++
					logger.Error("routines.memory_health: TriggerConsolidation",
						"workspace_id", sc.WorkspaceID, "crew_id", sc.CrewID, "error", perr)
				}
			}
		case keeper.DecisionEscalate:
			sum.EscalatedToInbox++
			if perr := persister.WriteInboxItem(ctx, sc.WorkspaceID, sc.CrewID, res.Reason, false); perr != nil {
				sum.Errors++
				logger.Error("routines.memory_health: WriteInboxItem",
					"workspace_id", sc.WorkspaceID, "crew_id", sc.CrewID, "error", perr)
			}
		}
	}

	logger.Info("routines.memory_health: sweep complete",
		"scanned", sum.ScannedScopes,
		"healthy", sum.HealthyAllowed,
		"consolidated", sum.AutoConsolidated,
		"escalated", sum.EscalatedToInbox,
		"errors", sum.Errors)
	return sum, nil
}

// Sentinel for the scheduler bootstrap to assert this package compiles
// without importing it for runtime use yet.
var _ = sql.ErrNoRows

// ValidRoutineKinds is the closed set used at the scheduler-bootstrap
// boundary. Mirrors the const block above.
func ValidRoutineKinds() []RoutineKind {
	return []RoutineKind{RoutineKindSkillReview, RoutineKindMemoryHealthCheck}
}

// String renders a RoutineKind for log lines / journal entries.
func (k RoutineKind) String() string { return string(k) }

// Validate checks the kind against the closed set. Used at the API
// boundary when the scheduler config references a routine kind.
func (k RoutineKind) Validate() error {
	for _, v := range ValidRoutineKinds() {
		if k == v {
			return nil
		}
	}
	return fmt.Errorf("routines: invalid kind %q (want %v)", k, ValidRoutineKinds())
}
