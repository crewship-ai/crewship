package api

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// safeIDPattern guards the crew_id segment we append to storagePath
// before MkdirAll. CUIDs (generateCUID) are guaranteed lowercase
// alphanumeric — no separators, no dot-prefixed segments — so a
// non-matching value can only arrive from a corrupted DB row or
// (theoretically) a future ID scheme. Rejecting at the path boundary
// keeps CodeQL's path-injection taint check happy AND adds genuine
// defense-in-depth: any future bug that lands a "../" sequence in a
// crew_id column would otherwise resolve outside storagePath. The
// 64-byte cap is generous (real CUIDs are 25 chars) but tight enough
// that a pathological row can't blow the OS PATH_MAX.
var safeIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

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
		// Belt-and-suspenders: the crew_id column is populated by API
		// paths that always call generateCUID(), so a value with a "/"
		// or ".." can't reach this row through normal flows. Validating
		// anyway keeps the path-construction below provably safe and
		// makes static analyzers (CodeQL go/path-injection) happy
		// without `//nolint` annotations.
		if !safeIDPattern.MatchString(crewID.String) {
			logger.Warn("mission outcome hook: rejecting crew_id with unsafe characters",
				"mission_id", missionID, "crew_id_len", len(crewID.String))
			return
		}

		// Prefer the stored completed_at when it parses; only fall back
		// to time.Now() when the column is NULL/empty. Silently
		// rewriting a present-but-unparseable value would drift the
		// lesson's captured_at to hook execution time and could
		// reorder outcomes on retries. Multi-layout parser handles
		// the legacy `datetime('now')` shape (SQLite's space-
		// separated form without 'T' and without timezone) alongside
		// the modern RFC3339 writes.
		completedTime := time.Now().UTC()
		if completedAt.Valid && completedAt.String != "" {
			if t, ok := parseStoredTimestamp(completedAt.String); ok {
				completedTime = t
			} else {
				logger.Debug("mission outcome hook: unparseable completed_at, using time.Now()",
					"mission_id", missionID, "raw", completedAt.String)
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

// parseStoredTimestamp accepts both RFC3339 (modern writes via
// time.Now().UTC().Format(time.RFC3339)) and SQLite's
// datetime('now')-shape ("2026-05-22 17:12:12") that v1-era migrations
// baked in as DEFAULT. Tries layouts in order of expected frequency;
// returns (time, true) on first hit, (zero, false) on no match.
//
// The legacy shape matters because some pre-v44 rows wrote completed_at
// via datetime('now') directly; recent terminal-state code paths use
// RFC3339, but a row aged out of an old DB could still carry the
// older form. See project_timestamp_defaults_followup.md note for the
// broader DEFAULT-format cleanup.
func parseStoredTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",     // sqlite datetime('now') default
		"2006-01-02 15:04:05.999", // sqlite datetime('now','subsec')
		"2006-01-02T15:04:05",     // RFC3339 minus zone
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// terminalStatusToLessonKindLocal mirrors consolidate.terminalStatusToLessonKind
// at the API boundary so the goroutine spawn can be skipped without
// crossing the package import path for a no-op decision. Keep in
// sync with the consolidate-package switch — both files reference
// the same documented terminal set (COMPLETED / DONE / FAILED /
// CANCELLED).
//
// Status is upper-cased + trimmed before matching so case-drift
// (a future caller passing "completed" or " COMPLETED ") still
// fires the hook. The consolidate-package switch already normalizes
// the same way for the same reason.
func terminalStatusToLessonKindLocal(status string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
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
