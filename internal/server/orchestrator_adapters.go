package server

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/presence"
)

// Three tiny adapters that wire orchestrator's narrow integration
// interfaces (HookDispatcher, ApprovalGate, EpisodicRecaller) to the
// real packages. They live in the server package because that's the
// one spot that already imports all of them — the orchestrator itself
// must stay decoupled or it'd pull in internal/api via the import
// graph and form a cycle.

// hooksAdapter plugs the hooks package into orchestrator.HookDispatcher.
// Events map 1:1 to hooks.Event; EventContext fields are translated at
// the boundary so the orchestrator doesn't need to know about
// hooks.EventContext.
type hooksAdapter struct {
	db    *sql.DB
	journ journal.Emitter
}

func newHooksAdapter(db *sql.DB, j journal.Emitter) *hooksAdapter {
	return &hooksAdapter{db: db, journ: j}
}

func (a *hooksAdapter) Dispatch(ctx context.Context, event string, ec orchestrator.HookEventContext) error {
	return hooks.Dispatch(ctx, a.db, a.journ, hooks.Event(event), hooks.EventContext{
		WorkspaceID: ec.WorkspaceID,
		CrewID:      ec.CrewID,
		AgentID:     ec.AgentID,
		MissionID:   ec.MissionID,
		ToolName:    ec.ToolName,
		Severity:    ec.Severity,
		Payload:     ec.Payload,
	})
}

// approvalGateAdapter wraps harbormaster.Gate so orchestrator.ApprovalGate
// has a single point of entry. The Evaluator is configured with the
// default rule set (destructive ops, cost thresholds, production
// hostnames); workspace admins extend it via DB-backed rules in a
// follow-up iteration.
type approvalGateAdapter struct {
	db        *sql.DB
	journ     journal.Emitter
	evaluator *harbormaster.Evaluator
}

func newApprovalGateAdapter(db *sql.DB, j journal.Emitter) *approvalGateAdapter {
	return &approvalGateAdapter{
		db:        db,
		journ:     j,
		evaluator: harbormaster.NewEvaluatorWithDefaults(),
	}
}

func (a *approvalGateAdapter) Check(ctx context.Context, in orchestrator.ApprovalCheckInput) (orchestrator.ApprovalDecision, error) {
	mode := harbormaster.ModeNone
	switch in.Mode {
	case "async":
		mode = harbormaster.ModeAsync
	case "sync":
		mode = harbormaster.ModeSync
	}
	dec, err := harbormaster.Gate(ctx, a.db, a.journ, a.evaluator, harbormaster.GateInput{
		WorkspaceID: in.WorkspaceID,
		CrewID:      in.CrewID,
		AgentID:     in.AgentID,
		MissionID:   in.MissionID,
		RequestedBy: in.UserID,
		Tool:        in.Tool,
		Args:        in.Args,
		Mode:        mode,
	})
	if err != nil {
		return orchestrator.ApprovalDecision{}, err
	}
	return orchestrator.ApprovalDecision{
		// Required=true means the gate matched at least one rule and an
		// enqueue happened; NotGated=true on Decision means no rule hit.
		Required:  !dec.NotGated,
		Approved:  dec.Approved,
		Denied:    dec.Denied,
		Pending:   dec.Pending,
		RequestID: dec.RequestID,
		Reason:    dec.Reason,
	}, nil
}

// episodicRecallAdapter bridges orchestrator.EpisodicRecaller to the
// episodic package. Role maps via episodic.ScopeForRole so LEAD /
// COORDINATOR get crew-shared scope, everything else gets own.
// Embedder is injected at construction time — nil embedder returns an
// empty recall silently (used when Ollama isn't reachable so runs
// don't fail on recall timeouts).
type episodicRecallAdapter struct {
	db       *sql.DB
	embedder episodic.Embedder
}

func newEpisodicRecallAdapter(db *sql.DB, embedder episodic.Embedder) *episodicRecallAdapter {
	return &episodicRecallAdapter{db: db, embedder: embedder}
}

// presenceAdapter bridges orchestrator.PresenceTracker to the presence
// package. Before this adapter existed the orchestrator emitted
// agent.status_change journal entries directly but never wrote the
// underlying agent_status row, so /crows-nest and
// /api/v1/presence/roster always returned empty. Track() calls
// presence.Upsert which both writes the row and emits the matching
// journal entry on real transitions (idempotent on same-status).
type presenceAdapter struct {
	db     *sql.DB
	journ  journal.Emitter
	logger *slog.Logger
}

func newPresenceAdapter(db *sql.DB, j journal.Emitter, logger *slog.Logger) *presenceAdapter {
	return &presenceAdapter{db: db, journ: j, logger: logger}
}

func (a *presenceAdapter) Track(ctx context.Context, in orchestrator.PresenceInput) error {
	err := presence.Upsert(ctx, a.db, a.journ, presence.Snapshot{
		AgentID:     in.AgentID,
		WorkspaceID: in.WorkspaceID,
		CrewID:      in.CrewID,
		MissionID:   in.MissionID,
		Status:      presence.Status(in.Status),
		Details:     in.Details,
	})
	if err != nil && a.logger != nil {
		// Watch Roster updates are best-effort — log but don't
		// propagate. A DB blip shouldn't abort an agent run just
		// because we couldn't flip the roster row.
		a.logger.Warn("presence track failed", "err", err,
			"agent_id", in.AgentID, "status", in.Status)
	}
	return nil
}

// memoryMetricsAdapter wraps the DB directly for the two cheap
// aggregate queries used by the nudge + cost-awareness blocks. Kept
// separate from the journal package because both queries span two
// tables and are read-only — nothing in internal/journal wants to
// grow a "count aggregate" API just for this consumer.
type memoryMetricsAdapter struct {
	db *sql.DB
}

func newMemoryMetricsAdapter(db *sql.DB) *memoryMetricsAdapter {
	return &memoryMetricsAdapter{db: db}
}

func (a *memoryMetricsAdapter) EntriesSinceLastMemoryUpdate(ctx context.Context, workspaceID, agentID string) (int64, error) {
	var n int64
	err := a.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM journal_entries
		 WHERE workspace_id = ? AND agent_id = ?
		   AND ts > COALESCE(
		     (SELECT MAX(ts) FROM journal_entries
		        WHERE workspace_id = ? AND agent_id = ? AND entry_type = 'memory.updated'),
		     datetime('now','-30 days'))`,
		workspaceID, agentID, workspaceID, agentID).Scan(&n)
	return n, err
}

func (a *memoryMetricsAdapter) AgentSpendLast24h(ctx context.Context, workspaceID, agentID string) (float64, int64, int64, error) {
	var (
		usd    float64
		tokens int64
		calls  int64
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0),
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COUNT(*)
		  FROM cost_ledger
		 WHERE workspace_id = ? AND agent_id = ?
		   AND ts >= datetime('now','-24 hours')`,
		workspaceID, agentID).Scan(&usd, &tokens, &calls)
	return usd, tokens, calls, err
}

func (a *episodicRecallAdapter) Recall(ctx context.Context, in orchestrator.EpisodicRecallInput) (string, error) {
	if a.embedder == nil {
		return "", nil
	}
	scope := episodic.ScopeForRole(in.Role)
	// HybridRecall fuses BM25 (FTS5) + cosine via Reciprocal Rank
	// Fusion so paraphrased queries AND keyword-exact queries both
	// land the right memories. Degrades gracefully to dense-only if
	// the FTS5 index is missing (pre-migration-55 DBs).
	hits, err := episodic.HybridRecall(ctx, a.db, a.embedder, episodic.Query{
		WorkspaceID: in.WorkspaceID,
		CrewID:      in.CrewID,
		AgentID:     in.AgentID,
		Scope:       scope,
		QueryText:   in.Query,
		K:           5,
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "", nil
	}
	return episodic.RenderInjection(hits, in.MaxChars), nil
}
