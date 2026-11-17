package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// orchestratorAuditAdapter bridges the orchestrator's narrow AuditEmitter
// interface to api.WriteAuditLog. The orchestrator intentionally depends
// on a narrow interface it owns so it doesn't pull in internal/api (which
// imports internal/orchestrator — a direct dependency would cycle). This
// file lives in the server package — the sole place that knows about
// both — and forwards the call at the wire boundary.
//
// The journal argument to WriteAuditLog is deliberately nil here: the
// orchestrator already emits its own richer, typed Crow's Nest entries
// for a run (exec.command, chat.user_message, …) via SetJournal /
// orchestratorJournalAdapter. Passing a journal emitter through here too
// would additionally dual-write a generic audit.entity_* entry for the
// same event — same "pass nil to skip the journal dual-write" pattern
// admin_reencrypt.go already uses for the same reason.
type orchestratorAuditAdapter struct {
	db      *sql.DB
	journal journal.Emitter
}

func newOrchestratorAuditAdapter(db *sql.DB, emitters ...journal.Emitter) *orchestratorAuditAdapter {
	a := &orchestratorAuditAdapter{db: db}
	if len(emitters) > 0 {
		a.journal = emitters[0]
	}
	return a
}

func (a *orchestratorAuditAdapter) RecordAudit(ctx context.Context, action, entityType, entityID, userID, workspaceID string, metadata map[string]any) {
	if a == nil || a.db == nil {
		return
	}
	goapi.WriteAuditLog(ctx, a.db, nil, action, entityType, entityID, userID, workspaceID, metadata)
}

// DELIVERED is scoped to the actual agent/run after the file preflight passed.
// It must not update last_used_at, which remains a separate USE signal.
func (a *orchestratorAuditAdapter) RecordNativeCredentialDelivery(ctx context.Context, credentialID, agentID, runID string) error {
	if a == nil || a.db == nil {
		return errors.New("credential audit database unavailable")
	}
	if agentID == "" || runID == "" {
		return errors.New("native credential delivery requires agent and run attribution")
	}
	return goapi.RecordCredentialEvent(ctx, a.db, slog.Default(), credentialID,
		goapi.AuditEventDelivered, agentID, "", map[string]any{
			"run_id": runID, "source": "native_preflight", "channel": "file", "authentication": "unknown",
		})
}

func (a *orchestratorAuditAdapter) RecordCodexSandboxPreflight(ctx context.Context, e provider.CodexSandboxEvidence) error {
	if a == nil || a.db == nil {
		return errors.New("Codex sandbox audit database unavailable")
	}
	return goapi.RecordCodexSandboxPreflight(ctx, a.db, a.journal, e)
}
