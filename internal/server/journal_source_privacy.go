package server

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

// A workspace journal channel cannot carry a private chat source projection.
// The existing source handler emits its data-free authorized chat hint; allowed
// readers still retrieve the journal row through its current audience fence.
// This read runs in the existing bridge drain, never on the commit observer.
func newJournalWSBridgeForSources(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *journalWSBridge {
	return newJournalWSBridgeWithFilter(hub, logger, func(entry journal.Entry) bool { return allowBroadSourceJournal(context.Background(), db, entry) })
}
func allowBroadSourceJournal(ctx context.Context, db *sql.DB, entry journal.Entry) bool {
	sourceID, _ := entry.Refs["escalation_id"].(string)
	if sourceID == "" {
		return true
	}
	var allowed bool
	err := db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM escalations e WHERE e.workspace_id=? AND e.id=? AND `+chataudience.VerifiedTextSourceSQL+`)`, entry.WorkspaceID, sourceID).Scan(&allowed)
	return err == nil && allowed
}
