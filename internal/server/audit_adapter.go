package server

import (
	"context"
	"database/sql"

	goapi "github.com/crewship-ai/crewship/internal/api"
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
	db *sql.DB
}

func newOrchestratorAuditAdapter(db *sql.DB) *orchestratorAuditAdapter {
	return &orchestratorAuditAdapter{db: db}
}

func (a *orchestratorAuditAdapter) RecordAudit(ctx context.Context, action, entityType, entityID, userID, workspaceID string, metadata map[string]any) {
	if a == nil || a.db == nil {
		return
	}
	goapi.WriteAuditLog(ctx, a.db, nil, action, entityType, entityID, userID, workspaceID, metadata)
}
