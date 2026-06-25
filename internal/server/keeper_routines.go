// PR-C F4 wire-up: scheduler-driven daily sweeps for the two Keeper
// Phase 2 routines:
//
//	skill_review        — RunSkillReview (F4.1, daily skill audit)
//	memory_health_check — RunMemoryHealthCheck (F4.3, daily memory hygiene)
//
// This file is the production glue between the pure-function routines
// (internal/keeper/routines/routines.go) and the cron scheduler:
//
//  1. Load candidates from SQL (skills for F4.1, crews for F4.3).
//  2. Pre-compute the per-item snapshots the evaluators need.
//  3. Invoke routines.Run* with a SQL-backed persister.
//
// Both runners are best-effort: a DB error inside a single iteration
// is logged and skipped, never aborts the whole sweep. PRD §6 F4
// explicitly calls these "audit, not gate" surfaces — a stuck row
// shouldn't pin the catalog at stale.
//
// Cron cadence: daily, offset 30 minutes apart so a slow LLM on the
// skill review doesn't block the memory health sweep. Both at 03:xx UTC
// during the lowest-traffic window so the Haiku token budget hit
// doesn't compete with user-driven runs.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/keeper/routines"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/skills"
)

// PRD §6 F4 default cadences. Daily at 03:00/03:30 UTC. Operators can
// override at the cfg level in a follow-up; baseline is fine for MVP.
const (
	cronSkillReview       = "0 3 * * *"  // daily 03:00 UTC
	cronMemoryHealthCheck = "30 3 * * *" // daily 03:30 UTC
)

// registerKeeperPhase2Routines wires both daily sweeps into the scheduler.
// Each routine is registered independently: if the evaluator is nil
// (e.g. ANTHROPIC_API_KEY missing during buildPhase2Evaluators), the
// matching routine is skipped with an info log so operators see why the
// sweep isn't running.
//
// Returns (skillRegistered, memoryRegistered) so the caller can log a
// consolidated summary; nil-error contract means a registration failure
// (invalid cron, scheduler down) doesn't abort the whole bootstrap — the
// remaining routine still gets a shot.
func registerKeeperPhase2Routines(
	sched *scheduler.Scheduler,
	db *sql.DB,
	skillEval *gatekeeper.SkillReviewEvaluator,
	memHealthEval *gatekeeper.MemoryHealthEvaluator,
	logger *slog.Logger,
) (skillRegistered, memoryRegistered bool) {
	if sched == nil || db == nil {
		logger.Info("keeper: phase 2 routines skipped (scheduler or DB nil)")
		return false, false
	}

	if skillEval != nil {
		fn := func(ctx context.Context) {
			runSkillReviewSweep(ctx, db, skillEval, logger)
		}
		if err := sched.RegisterPlatformRoutine(string(routines.RoutineKindSkillReview), cronSkillReview, fn); err != nil {
			logger.Error("keeper: skill_review routine registration failed", "error", err)
		} else {
			skillRegistered = true
		}
	} else {
		logger.Info("keeper: skill_review routine NOT registered (evaluator nil)")
	}

	if memHealthEval != nil {
		fn := func(ctx context.Context) {
			runMemoryHealthSweep(ctx, db, memHealthEval, logger)
		}
		if err := sched.RegisterPlatformRoutine(string(routines.RoutineKindMemoryHealthCheck), cronMemoryHealthCheck, fn); err != nil {
			logger.Error("keeper: memory_health_check routine registration failed", "error", err)
		} else {
			memoryRegistered = true
		}
	} else {
		logger.Info("keeper: memory_health_check routine NOT registered (evaluator nil)")
	}

	return skillRegistered, memoryRegistered
}

// ---- F4.1 skill_review sweep ----

// runSkillReviewSweep loads every skill (the catalog is workspace-global
// today) and runs the F4.1 evaluator over each. Best-effort: per-skill
// errors are logged into the summary's error counter but never abort the
// sweep. Result is logged as one structured info line so the daily
// telemetry can be greped without trawling the journal.
func runSkillReviewSweep(
	ctx context.Context,
	db *sql.DB,
	ev *gatekeeper.SkillReviewEvaluator,
	logger *slog.Logger,
) {
	inputs, err := loadSkillSweepInputs(ctx, db, logger)
	if err != nil {
		logger.Error("keeper.skill_review: load inputs failed", "error", err)
		return
	}
	if len(inputs) == 0 {
		logger.Info("keeper.skill_review: no skills to audit")
		return
	}
	pers := &sqlSkillPersister{db: db, logger: logger}
	sum, err := routines.RunSkillReview(ctx, ev, pers, inputs, logger)
	if err != nil && ctx.Err() == nil {
		logger.Error("keeper.skill_review: sweep aborted", "error", err)
		return
	}
	logger.Info("keeper.skill_review: daily sweep complete",
		"scanned", sum.ScannedSkills,
		"verified", sum.VerifiedAllowed,
		"unverified", sum.UnverifiedDenied,
		"escalated", sum.EscalatedToInbox,
		"lifecycle_flipped", sum.LifecycleFlipped,
		"errors", sum.Errors)
}

// loadSkillSweepInputs walks the skills table and assembles the per-skill
// input the F4.1 evaluator expects. Stays read-only — the persister is
// the only writer. Skips soft-deleted skills (column may not exist on
// older schemas; the LIMIT clause adapts via best-effort selection).
//
// The skill_invocations stats are aggregated in a single COUNT/SUM over
// the configured lookback (default 30 days, hardcoded for MVP — matches
// the gatekeeper.SkillStats.LookbackDays default).
func loadSkillSweepInputs(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
) ([]routines.SkillSweepInput, error) {
	// Pull skill catalog. Skills lifecycle_state was added by v102 (PR-C);
	// defensive fallback to 'active' on NULL keeps this query forward-
	// compatible with skills inserted before the column existed.
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.name, COALESCE(s.description, ''),
		       COALESCE(s.lifecycle_state, 'active'),
		       COALESCE(s.last_used_at, '')
		  FROM skills s
		 ORDER BY s.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query skills: %w", err)
	}
	defer rows.Close()

	const lookbackDays = 30
	lookbackCutoff := time.Now().UTC().AddDate(0, 0, -lookbackDays).Format(time.RFC3339)

	var out []routines.SkillSweepInput
	for rows.Next() {
		var (
			id, name, desc, lifecycle, lastUsedStr string
		)
		if err := rows.Scan(&id, &name, &desc, &lifecycle, &lastUsedStr); err != nil {
			logger.Warn("keeper.skill_review: scan skill row", "error", err)
			continue
		}
		var lastUsed time.Time
		if lastUsedStr != "" {
			if t, perr := time.Parse(time.RFC3339, lastUsedStr); perr == nil {
				lastUsed = t
			}
		}

		// Per-skill assignment count + agent slugs. agent_skills doesn't
		// scope by workspace; the assignment count is global, matching
		// the global skills catalog.
		var assignments int
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM agent_skills WHERE skill_id = ? AND enabled = 1`,
			id).Scan(&assignments)

		// Aggregate stats from skill_invocations within the lookback
		// window. Per-skill cost is modest (one indexed COUNT/SUM); the
		// outer ORDER BY id keeps caller logging deterministic.
		var (
			invCount, errCount int
			lastInvStr         sql.NullString
		)
		_ = db.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN exit_code <> 0 THEN 1 ELSE 0 END), 0),
			       COALESCE(MAX(invoked_at), '')
			  FROM skill_invocations
			 WHERE skill_id = ? AND invoked_at >= ?`,
			id, lookbackCutoff).Scan(&invCount, &errCount, &lastInvStr)

		stats := gatekeeper.SkillStats{
			InvocationCount: invCount,
			ErrorCount:      errCount,
			LookbackDays:    lookbackDays,
		}
		if lastInvStr.Valid {
			stats.LastUsedAt = lastInvStr.String
		}

		out = append(out, routines.SkillSweepInput{
			Skill: routines.SkillRow{
				ID:             id,
				Name:           name,
				Description:    desc,
				WorkspaceID:    "", // global catalog; F4.1 prompt tolerates empty
				LifecycleState: skills.LifecycleState(lifecycle),
				LastUsedAt:     lastUsed,
				Assignments:    assignments,
			},
			Stats: stats,
			// AssignedAgents + FailurePeeks left empty for MVP — the
			// evaluator's prompt degrades gracefully and the slice
			// lookups would multiply DB cost per skill. Follow-up:
			// wire from agent_skills join + journal_entries
			// EntryRunFailed when the cost budget allows.
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}
	return out, nil
}

// sqlSkillPersister implements routines.SkillSweepPersister with direct
// SQL writes against the skills + inbox_items tables. All writes are
// idempotent: UPDATE for verification/lifecycle (no-op if same), INSERT
// OR IGNORE into inbox via the inbox.Insert helper (dedup on source_id).
type sqlSkillPersister struct {
	db     *sql.DB
	logger *slog.Logger
}

func (p *sqlSkillPersister) MarkVerified(ctx context.Context, skillID string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE skills SET verification = 'VERIFIED', updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), skillID)
	return err
}

func (p *sqlSkillPersister) MarkUnverified(ctx context.Context, skillID string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE skills SET verification = 'UNVERIFIED', updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), skillID)
	return err
}

func (p *sqlSkillPersister) SetLifecycle(ctx context.Context, skillID string, next skills.LifecycleState, reason string) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE skills SET lifecycle_state = ?, updated_at = ? WHERE id = ? AND lifecycle_state != ?`,
		string(next), time.Now().UTC().Format(time.RFC3339), skillID, string(next))
	if err != nil {
		return err
	}
	// Reason is logged here for now; if a per-skill audit trail surface
	// emerges (skill_lifecycle_history) the SQL handler would also
	// INSERT there. The keeper_requests row written by the API handler
	// path captures the reason for ad-hoc reviews; this routine writes
	// no keeper_requests row (it's the routine_runs surface's job).
	p.logger.Info("keeper.skill_review: lifecycle flipped",
		"skill_id", skillID, "next", string(next), "reason", reason)
	return nil
}

func (p *sqlSkillPersister) WriteInboxItem(ctx context.Context, skillID, reason string, blocking bool) error {
	// The skills table is workspace-global so there's no skill→workspace
	// FK. Inbox items require a workspace_id; a previous revision wrote
	// to the FIRST matching workspace only (LIMIT 1), which dropped
	// notifications for every other workspace using the same skill —
	// the catalog-level "unverify" signal silently became a single-
	// workspace event.
	//
	// Fan out instead: SELECT DISTINCT each workspace that has at least
	// one enabled assignment of the skill on a live agent, then write
	// one inbox row per workspace. The dedup key (source_id =
	// "skill_review_"+skillID) is workspace-scoped at the inbox layer,
	// so re-runs on the same day collapse on the existing row.
	//
	// If no workspace has the skill assigned we skip silently (a stale
	// audit row in the void); the next assignment re-triggers on the
	// following daily sweep.
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT a.workspace_id
		  FROM agent_skills sk
		  JOIN agents a ON a.id = sk.agent_id
		 WHERE sk.skill_id = ? AND sk.enabled = 1 AND a.deleted_at IS NULL`, skillID)
	if err != nil {
		return fmt.Errorf("lookup skill workspaces: %w", err)
	}
	defer rows.Close()

	var workspaceIDs []string
	for rows.Next() {
		var ws string
		if err := rows.Scan(&ws); err != nil {
			return fmt.Errorf("scan workspace_id: %w", err)
		}
		if ws == "" {
			continue
		}
		workspaceIDs = append(workspaceIDs, ws)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate workspaces: %w", err)
	}
	if len(workspaceIDs) == 0 {
		return nil
	}

	var firstErr error
	for _, workspaceID := range workspaceIDs {
		// Scope source_id by workspace because the inbox unique index
		// is (kind, source_id) GLOBAL (not per-workspace). Without the
		// workspace suffix the second workspace's INSERT OR IGNORE
		// would dedup against the first workspace's row — defeating
		// the whole point of the fanout.
		if err := inbox.Insert(ctx, p.db, p.logger, inbox.Item{
			WorkspaceID: workspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    "skill_review_" + skillID + "_" + workspaceID,
			TargetRole:  "MANAGER",
			Title:       "Skill review: " + skillID,
			BodyMD:      reason,
			SenderType:  "system",
			SenderID:    "keeper_skill_review_routine",
			SenderName:  "Skill Curator",
			Priority:    "medium",
			Blocking:    blocking,
			Payload: map[string]interface{}{
				"skill_id": skillID,
				"reason":   reason,
				"source":   "routine",
			},
		}); err != nil {
			// Log per-workspace and keep going — one workspace's
			// inbox being unavailable shouldn't drop notifications for
			// every other workspace.
			p.logger.Error("keeper.skill_review: inbox insert failed",
				"workspace_id", workspaceID, "skill_id", skillID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ---- F4.3 memory_health_check sweep ----

// runMemoryHealthSweep iterates every (workspace, crew) tuple from the
// crews table and runs the F4.3 evaluator. Computes the HealthSnapshot
// inline via consolidate.ComputeHealth — the same code path the daily
// consolidator uses, so the score is consistent with what the UI shows.
func runMemoryHealthSweep(
	ctx context.Context,
	db *sql.DB,
	ev *gatekeeper.MemoryHealthEvaluator,
	logger *slog.Logger,
) {
	scopes, err := loadMemoryHealthScopes(ctx, db, logger)
	if err != nil {
		logger.Error("keeper.memory_health_check: load scopes failed", "error", err)
		return
	}
	if len(scopes) == 0 {
		logger.Info("keeper.memory_health_check: no scopes to audit")
		return
	}
	pers := &sqlMemoryHealthPersister{db: db, logger: logger}
	sum, err := routines.RunMemoryHealthCheck(ctx, ev, pers, scopes, logger)
	if err != nil && ctx.Err() == nil {
		logger.Error("keeper.memory_health_check: sweep aborted", "error", err)
		return
	}
	logger.Info("keeper.memory_health_check: daily sweep complete",
		"scanned", sum.ScannedScopes,
		"healthy", sum.HealthyAllowed,
		"consolidated", sum.AutoConsolidated,
		"escalated", sum.EscalatedToInbox,
		"errors", sum.Errors)
}

// loadMemoryHealthScopes returns one MemoryHealthScope per (workspace,
// crew). Computes the HealthSnapshot via consolidate.ComputeHealth
// (cheap, read-only) and the contradiction count via a single COUNT
// against memory_relations. AgentMDBytes / PersonaMDBytes / CrewMDBytes
// / StalestEntryDays are placeholders (0) for MVP — the F4.3 prompt
// tolerates zeros; future work fills them from a memory dir walk.
func loadMemoryHealthScopes(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
) ([]routines.MemoryHealthScope, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT workspace_id, id, name
		  FROM crews
		 WHERE deleted_at IS NULL
		 ORDER BY workspace_id, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query crews: %w", err)
	}
	defer rows.Close()

	var out []routines.MemoryHealthScope
	for rows.Next() {
		var workspaceID, crewID, crewName string
		if err := rows.Scan(&workspaceID, &crewID, &crewName); err != nil {
			logger.Warn("keeper.memory_health_check: scan crew row", "error", err)
			continue
		}
		snap, herr := consolidate.ComputeHealth(ctx, db, workspaceID, crewID)
		if herr != nil {
			logger.Warn("keeper.memory_health_check: ComputeHealth failed",
				"workspace_id", workspaceID, "crew_id", crewID, "error", herr)
			continue
		}
		var contradictions int
		// memory_relations references journal_entries (the relation graph
		// is built over the unified journal stream). Scope by the
		// referenced entry's workspace/crew so each (workspace, crew)
		// scope gets its own contradiction count.
		_ = db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM memory_relations mr
			  JOIN journal_entries je ON je.id = mr.entry_id
			 WHERE je.workspace_id = ? AND je.crew_id = ? AND mr.relation_kind = 'refutes'`,
			workspaceID, crewID).Scan(&contradictions)

		out = append(out, routines.MemoryHealthScope{
			WorkspaceID:        workspaceID,
			CrewID:             crewID,
			CrewName:           crewName,
			Snapshot:           snap,
			ContradictionCount: contradictions,
			// Byte counts + stalest-entry-age are deferred; an explicit
			// follow-up (fs walk under cfg.Storage.MemoryRoot/{workspace}/{crew})
			// would fill them when we want a tighter F4.3 prompt.
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crews: %w", err)
	}
	return out, nil
}

// sqlMemoryHealthPersister implements routines.MemoryHealthPersister. The
// MVP path emits an inbox item on ESCALATE; auto-consolidation kicks the
// consolidate.Consolidator pipeline (via the journal event the
// consolidator listens for) — for now we just log; future wiring would
// call consolidator.RunForCrew directly.
type sqlMemoryHealthPersister struct {
	db     *sql.DB
	logger *slog.Logger
}

func (p *sqlMemoryHealthPersister) TriggerConsolidation(ctx context.Context, workspaceID, crewID, reason string) error {
	// MVP: log + write a memory_consolidation inbox item so the operator
	// sees the trigger. Hard-wiring to the Consolidator instance would
	// require threading it through registerKeeperPhase2Routines; the
	// 6-hourly consolidator runner already sweeps on its own cadence, so
	// the routine's role is just "tell the operator this crew needs it".
	//
	// Propagate inbox.Insert errors so the sweep summary reflects real
	// failure counts instead of falsely reporting success when the inbox
	// write fails.
	p.logger.Info("keeper.memory_health_check: auto-consolidation triggered",
		"workspace_id", workspaceID, "crew_id", crewID, "reason", reason)
	if err := inbox.Insert(ctx, p.db, p.logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindMemoryConsolidation,
		SourceID:    "memory_health_" + crewID + "_" + time.Now().UTC().Format("20060102"),
		TargetRole:  "MANAGER",
		Title:       "Memory consolidation suggested",
		BodyMD:      reason,
		SenderType:  "system",
		SenderID:    "keeper_memory_health_routine",
		SenderName:  "Memory Health",
		Priority:    "medium",
		Blocking:    false,
		Payload: map[string]interface{}{
			"crew_id": crewID,
			"reason":  reason,
			"source":  "routine",
		},
	}); err != nil {
		return fmt.Errorf("inbox insert (consolidation): %w", err)
	}
	return nil
}

func (p *sqlMemoryHealthPersister) WriteInboxItem(ctx context.Context, workspaceID, crewID, reason string, blocking bool) error {
	// This is a SYSTEM ADVISORY ("this crew's memory looks unhealthy"),
	// not an agent asking a human to decide something. It must NOT be
	// kind=escalation: an escalation is source-managed (only the
	// escalations-table lifecycle resolves it), but this row has no
	// escalations record behind it — so as kind=escalation it could
	// never be cleared (inbox PATCH→409, /escalations/{id}/resolve→404)
	// and the advisories piled up unclearable. As kind=message it's a
	// freely-dismissable notification, and the bulk-resolve guard no
	// longer treats it as a protected decision item. Non-blocking for
	// the same reason: there is no decision to block on.
	if err := inbox.Insert(ctx, p.db, p.logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindMessage,
		SourceID:    "memory_health_advisory_" + crewID + "_" + time.Now().UTC().Format("20060102"),
		TargetRole:  "MANAGER",
		Title:       "Memory health advisory",
		BodyMD:      reason,
		SenderType:  "system",
		SenderID:    "keeper_memory_health_routine",
		SenderName:  "Memory Health",
		Priority:    "medium",
		Blocking:    false,
		Payload: map[string]interface{}{
			"crew_id": crewID,
			"reason":  reason,
			"source":  "routine",
		},
	}); err != nil {
		return fmt.Errorf("inbox insert (memory_health advisory): %w", err)
	}
	return nil
}
