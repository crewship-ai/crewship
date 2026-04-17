package server

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/episodic"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
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
	db     *sql.DB
	journ  journal.Emitter
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

func (a *episodicRecallAdapter) Recall(ctx context.Context, in orchestrator.EpisodicRecallInput) (string, error) {
	if a.embedder == nil {
		return "", nil
	}
	scope := episodic.ScopeForRole(in.Role)
	hits, err := episodic.Recall(ctx, a.db, a.embedder, episodic.Query{
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
