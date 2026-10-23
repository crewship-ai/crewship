package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// sidecarRecreatedJournal turns the container provider's report that it
// replaced a crew runtime serving an outdated crewship-sidecar (#3964) into a
// sidecar.recreated journal entry: the operator-visible counterpart of
// sidecar.stale, saying the condition was resolved without them.
//
// The provider knows the crew only by id, so the workspace scope is read from
// the crews row. Best-effort: the replacement already happened, and a failed
// journal write must not fail the run that triggered it.
func sidecarRecreatedJournal(db *sql.DB, emitter journal.Emitter, logger *slog.Logger) func(context.Context, provider.SidecarRecreatedEvent) {
	return func(ctx context.Context, ev provider.SidecarRecreatedEvent) {
		if db == nil || emitter == nil {
			return
		}
		// The run that triggered the replacement may be cancelled right
		// after; the record of what happened to the crew must still land.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		var workspaceID, name string
		err := db.QueryRowContext(ctx,
			`SELECT workspace_id, COALESCE(name, '') FROM crews WHERE id = ?`, ev.CrewID).Scan(&workspaceID, &name)
		if err != nil {
			logger.Warn("sidecar recreated: crew lookup failed; not journaled", "crew_id", ev.CrewID, "error", err)
			return
		}
		previous := ev.PreviousSidecarHash
		if previous == "" {
			previous = "unknown (created before sidecar stamping)"
		}
		if _, err := emitter.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			CrewID:      ev.CrewID,
			Type:        journal.EntrySidecarRecreated,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSystem,
			ActorID:     "container-provider",
			Summary: fmt.Sprintf(
				"%s's crew container was recreated to pick up the current crewship-sidecar (%s → %s); no action needed.",
				displayName(name, ev.CrewSlug), previous, ev.SidecarHash),
			Payload: map[string]any{
				"previous_sidecar_hash": ev.PreviousSidecarHash,
				"sidecar_hash":          ev.SidecarHash,
				"previous_container_id": provider.ShortID(ev.PreviousContainerID),
				"container_id":          provider.ShortID(ev.ContainerID),
				"crew_slug":             ev.CrewSlug,
			},
			Refs: map[string]any{"crew_id": ev.CrewID, "container_id": ev.ContainerID},
		}); err != nil {
			logger.Warn("sidecar recreated: journal emit failed", "crew_id", ev.CrewID, "error", err)
		}
	}
}
