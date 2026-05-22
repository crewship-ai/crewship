package consolidate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MissionOutcome carries the fields the lesson-writer needs to render
// a single terminal-state mission as an entry in the crew-shared
// lessons.md. The struct is deliberately flat (no embedded DB row
// type, no orchestrator types) so the caller can populate it from
// either a query result or a constructed test fixture without
// pulling in package internal/api.
//
// All fields are required EXCEPT CompletedAt — a zero CompletedAt
// triggers time.Now() inside the writer, matching the behavior of
// LessonEntry.CapturedAt. Empty MissionID is rejected at the
// boundary because the lesson ID is derived from it; a degenerate
// "mission_outcome_" prefix would collide every outcome into a
// single row.
type MissionOutcome struct {
	MissionID   string    // CUID — used as the lesson ID anchor for idempotency
	Identifier  string    // human-readable (ENG-1, DEV-2) — rendered in the rule body
	Title       string    // mission title — rendered after the identifier
	Status      string    // terminal status: COMPLETED | DONE | FAILED | CANCELLED. Other values no-op.
	LeadSlug    string    // crew LEAD slug — recorded in the context note
	CompletedAt time.Time // when the terminal transition happened; zero → now
}

// terminalStatusToLessonKind maps mission terminal states to lesson
// polarity. The mapping is exhaustive for the documented terminal set
// (COMPLETED, DONE, FAILED, CANCELLED). Anything else returns (zero,
// false) so the caller treats it as a non-terminal no-op rather than
// silently landing a neutral entry under an unexpected status.
func terminalStatusToLessonKind(status string) (LessonKind, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "COMPLETED", "DONE":
		return LessonKindPositive, true
	case "FAILED":
		return LessonKindNegative, true
	case "CANCELLED":
		return LessonKindNeutral, true
	default:
		return "", false
	}
}

// EmitMissionOutcomeLesson is the F4.5 hook that wires mission
// terminal-state transitions into the crew-shared lessons.md.
// Callers in internal/api invoke this AFTER their status-mutation
// transaction commits — the write is best-effort and a failure must
// not be returned through the operator's API response, only logged.
//
// Behavior summary:
//
//   - status COMPLETED or DONE → kind=positive
//   - status FAILED            → kind=negative
//   - status CANCELLED         → kind=neutral
//   - any other status         → no-op (return nil, no file write)
//
// The lesson ID is "mission_outcome_<missionID>" so re-firing the
// hook for the same mission is idempotent (replace-in-place via
// WriteCrewLesson). Empty MissionID is rejected because that
// invariant cannot be enforced post-fact: two different missions
// with empty IDs would collide on the same lesson row.
//
// The hook is safe to call on every status transition the API
// surfaces — non-terminal statuses (PLANNING, IN_PROGRESS, REVIEW,
// BACKLOG, TODO) clean-no-op and do not even open the lessons.md
// file. This keeps the call-site in mission_handler_mutate.go a
// single line without status-discriminator branches.
func EmitMissionOutcomeLesson(ctx context.Context, crewSharedMemoryDir string, mo MissionOutcome) error {
	kind, terminal := terminalStatusToLessonKind(mo.Status)
	if !terminal {
		return nil
	}
	if strings.TrimSpace(mo.MissionID) == "" {
		return errors.New("mission outcome: mission_id is required (idempotency anchor)")
	}
	if mo.CompletedAt.IsZero() {
		mo.CompletedAt = time.Now().UTC()
	}
	entry := LessonEntry{
		ID:          "mission_outcome_" + mo.MissionID,
		Kind:        kind,
		CapturedAt:  mo.CompletedAt,
		Source:      LessonSourceMissionOutcome,
		Rule:        buildMissionOutcomeRule(mo),
		ContextNote: buildMissionOutcomeContext(mo),
	}
	return WriteCrewLesson(ctx, crewSharedMemoryDir, entry)
}

// buildMissionOutcomeRule renders the mechanically-derived rule body
// from mission fields. The shape is intentionally deterministic — no
// LLM rewrite happens here, so the same input produces byte-identical
// output across runs. That property keeps the idempotency tests honest
// (a re-run of the helper with the same struct produces the same row).
//
// Format: "<IDENTIFIER> <past-tense status>: <title>"
//
// Example:
//
//	"ENG-1 completed: Ping google.com 5 times and save results"
//	"DEV-4 failed: Trace DNS resolution for 3 domains"
//
// We use "completed" for both COMPLETED and DONE so the operator
// reading lessons.md doesn't see two different past-tense renderings
// of the same logical outcome. Distinguishing them in the source
// status is fine; flattening them in the rule body keeps the LEAD
// prompt's outcomes section readable.
func buildMissionOutcomeRule(mo MissionOutcome) string {
	verb := pastTenseForStatus(mo.Status)
	identifier := strings.TrimSpace(mo.Identifier)
	if identifier == "" {
		identifier = mo.MissionID
	}
	title := strings.TrimSpace(mo.Title)
	if title == "" {
		return fmt.Sprintf("%s %s", identifier, verb)
	}
	return fmt.Sprintf("%s %s: %s", identifier, verb, title)
}

// buildMissionOutcomeContext renders the LEAD-attribution string that
// lands in the YAML `context:` field. The boot prompt assembly later
// surfaces this verbatim, so the format needs to read well in a
// system-prompt section: "<status> · LEAD=<slug>".
//
// We deliberately keep this short so the section stays inside the
// CREW SHARED MEMORY budget (40% of total memory budget); a verbose
// per-entry context would burn tokens without commensurate signal.
func buildMissionOutcomeContext(mo MissionOutcome) string {
	parts := make([]string, 0, 2)
	if s := strings.TrimSpace(mo.Status); s != "" {
		parts = append(parts, strings.ToUpper(s))
	}
	if s := strings.TrimSpace(mo.LeadSlug); s != "" {
		parts = append(parts, "LEAD="+s)
	}
	return strings.Join(parts, " · ")
}

func pastTenseForStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "COMPLETED", "DONE":
		return "completed"
	case "FAILED":
		return "failed"
	case "CANCELLED":
		return "cancelled"
	default:
		return "transitioned"
	}
}
