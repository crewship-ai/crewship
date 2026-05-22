package api

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// emitMissionOutcomeLessonAsync is the F4.5 mission-outcomes-to-crew-
// memory hook called from terminal-state mutation paths in
// mission_handler_mutate.go and issue_handler_workflow.go. It runs
// AFTER the SQL transaction has committed — a write failure here must
// not roll back the operator's intentional status transition, so the
// hook returns nothing and logs at WARN on failure.
//
// The function fires its work in a fresh goroutine with a detached
// context (a copy of the request context's values minus its deadline
// — see context.WithoutCancel) so a slow filesystem doesn't stall
// the HTTP response. The lesson write itself is fast (single flock +
// single rename), so practical latency is sub-millisecond on a local
// SSD; the goroutine indirection exists to insulate the API thread
// from the worst case (NFS, slow disk).
//
// Skips silently (log only at DEBUG) when:
//   - storagePath is empty: handler wasn't wired with SetStoragePath
//   - mission row read fails: mission could have been deleted between
//     the status update and this call
//   - status isn't a terminal one: consolidate.EmitMissionOutcomeLesson
//     itself returns nil on non-terminal, this just avoids the trip
//
// On a successful write, no log line is emitted — the YAML row on disk
// IS the audit trail. Operators chasing "did this hook fire" can grep
// /crew/shared/.memory/lessons.md for the mission_outcome_<id> row.
func emitMissionOutcomeLessonAsync(
	ctx context.Context,
	db *sql.DB,
	storagePath string,
	missionID string,
	newStatus string,
	logger *slog.Logger,
) {
	if storagePath == "" {
		// Handler wasn't wired with SetStoragePath. This is expected
		// in unit-test paths that construct the handler directly; do
		// not log noisily on every status update in those flows.
		return
	}
	if _, terminal := terminalStatusToLessonKindLocal(newStatus); !terminal {
		return
	}

	// Detach from the request context: the caller is about to send
	// the response back, and we don't want a client disconnect to
	// race-cancel the lesson write.
	detached := context.WithoutCancel(ctx)

	go func() {
		// Bound the work; 5s is plenty for a single file write but
		// keeps an alarmingly slow disk from leaking goroutines.
		workCtx, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()

		var (
			crewID, identifier, title, status, leadSlug sql.NullString
			completedAt                                 sql.NullString
		)
		err := db.QueryRowContext(workCtx, `
			SELECT m.crew_id, m.identifier, m.title, m.status,
			       a.slug, m.completed_at
			FROM missions m
			LEFT JOIN agents a ON a.id = m.lead_agent_id
			WHERE m.id = ?`,
			missionID).Scan(&crewID, &identifier, &title, &status, &leadSlug, &completedAt)
		if err != nil {
			logger.Debug("mission outcome hook: mission row not readable",
				"mission_id", missionID, "error", err)
			return
		}

		if !crewID.Valid || crewID.String == "" {
			logger.Debug("mission outcome hook: mission has no crew_id",
				"mission_id", missionID)
			return
		}

		completedTime := time.Now().UTC()
		if completedAt.Valid && completedAt.String != "" {
			if t, parseErr := time.Parse(time.RFC3339, completedAt.String); parseErr == nil {
				completedTime = t.UTC()
			}
		}

		crewSharedDir := filepath.Join(storagePath, "crews", crewID.String, "shared", ".memory")

		mo := consolidate.MissionOutcome{
			MissionID:   missionID,
			Identifier:  identifier.String,
			Title:       title.String,
			Status:      newStatus,
			LeadSlug:    leadSlug.String,
			CompletedAt: completedTime,
		}
		if err := consolidate.EmitMissionOutcomeLesson(workCtx, crewSharedDir, mo); err != nil {
			logger.Warn("mission outcome hook: lesson write failed (status transition unaffected)",
				"mission_id", missionID,
				"crew_id", crewID.String,
				"status", newStatus,
				"error", err)
		}
	}()
}

// terminalStatusToLessonKindLocal mirrors consolidate.terminalStatusToLessonKind
// at the API boundary so the goroutine spawn can be skipped without
// crossing the package import path for a no-op decision. Keep in
// sync with the consolidate-package switch — both files reference
// the same documented terminal set (COMPLETED / DONE / FAILED /
// CANCELLED).
func terminalStatusToLessonKindLocal(status string) (string, bool) {
	switch status {
	case "COMPLETED", "DONE":
		return "positive", true
	case "FAILED":
		return "negative", true
	case "CANCELLED":
		return "neutral", true
	default:
		return "", false
	}
}
