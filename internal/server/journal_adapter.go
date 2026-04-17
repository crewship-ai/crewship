package server

import (
	"context"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// orchestratorJournalAdapter bridges orchestrator.JournalEmitter to the
// real journal.Emitter. The orchestrator intentionally depends on a
// narrow interface it owns so it doesn't pull in internal/journal (which
// would create an import cycle once internal/api depends on both). This
// file lives in the server package — the sole place that knows about
// both — and translates the typed-but-small orchestrator.JournalEntry
// into a full journal.Entry at the wire boundary.
type orchestratorJournalAdapter struct {
	emitter journal.Emitter
}

func newOrchestratorJournalAdapter(e journal.Emitter) *orchestratorJournalAdapter {
	return &orchestratorJournalAdapter{emitter: e}
}

func (a *orchestratorJournalAdapter) Emit(ctx context.Context, e orchestrator.JournalEntry) (string, error) {
	if a.emitter == nil {
		return "", nil
	}
	return a.emitter.Emit(ctx, journal.Entry{
		WorkspaceID: e.WorkspaceID,
		CrewID:      e.CrewID,
		AgentID:     e.AgentID,
		MissionID:   e.MissionID,
		Type:        journal.EntryType(e.Type),
		Severity:    severityOrDefault(e.Severity),
		ActorType:   actorOrDefault(e.ActorType),
		ActorID:     e.ActorID,
		Summary:     e.Summary,
		Payload:     e.Payload,
		Refs:        e.Refs,
	})
}

// severityOrDefault fills in "info" when the orchestrator caller leaves
// Severity empty, so handlers in the orchestrator can stay terse for
// routine events (exec.command, container.metrics) without every call
// site repeating the string literal.
func severityOrDefault(s string) journal.Severity {
	if s == "" {
		return journal.SeverityInfo
	}
	return journal.Severity(s)
}

// actorOrDefault fills in "orchestrator" because every emit from this
// package by definition comes from the orchestrator layer. Callers can
// still override with "sidecar" or "system" when the logical actor is
// elsewhere.
func actorOrDefault(s string) journal.ActorType {
	if s == "" {
		return journal.ActorOrchestrator
	}
	return journal.ActorType(s)
}
