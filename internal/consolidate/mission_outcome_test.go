package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEmitMissionOutcomeLesson_CompletedIsPositive verifies the
// status → LessonKind mapping for the most common terminal state.
// A mission that COMPLETED carries "worth repeating" signal — kind
// must be positive so downstream consumers (LessonsLearnedCard, the
// LEAD boot prompt outcomes section) can filter positive vs negative
// outcomes without re-parsing the body.
func TestEmitMissionOutcomeLesson_CompletedIsPositive(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID:   "m_eng_1",
		Identifier:  "ENG-1",
		Title:       "Ping google.com 5 times and save results",
		Status:      "COMPLETED",
		LeadSlug:    "eva",
		CompletedAt: time.Date(2026, 5, 22, 14, 0, 0, 0, time.UTC),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
		t.Fatalf("EmitMissionOutcomeLesson: %v", err)
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.Kind != LessonKindPositive {
		t.Errorf("COMPLETED → expected kind=positive, got %q", got.Kind)
	}
	if got.Source != LessonSourceMissionOutcome {
		t.Errorf("expected source=mission_outcome, got %q", got.Source)
	}
	if got.ID != "mission_outcome_m_eng_1" {
		t.Errorf("expected ID 'mission_outcome_m_eng_1', got %q", got.ID)
	}
	if !strings.Contains(got.Rule, "ENG-1") || !strings.Contains(got.Rule, "Ping google.com") {
		t.Errorf("rule must reference identifier + title; got %q", got.Rule)
	}
}

// TestEmitMissionOutcomeLesson_DoneIsPositive — the issue-tracker
// REVIEW→DONE path uses status="DONE" instead of "COMPLETED". Both
// terminal-positive states must map to the same kind so the LEAD
// prompt section is consistent across handler entry points.
func TestEmitMissionOutcomeLesson_DoneIsPositive(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID: "m_qua_3", Identifier: "QUA-3",
		Title: "Log parser", Status: "DONE", LeadSlug: "beacon",
		CompletedAt: time.Now().UTC(),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != LessonKindPositive {
		t.Errorf("DONE → expected kind=positive, got %+v", entries)
	}
}

// TestEmitMissionOutcomeLesson_FailedIsNegative is the failure
// counterpart — FAILED must map to negative so the LEAD's
// "[CREW OUTCOMES]" section visibly marks the mission as an anti-pattern
// the team should learn from.
func TestEmitMissionOutcomeLesson_FailedIsNegative(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID:   "m_dev_4",
		Identifier:  "DEV-4",
		Title:       "Trace DNS resolution",
		Status:      "FAILED",
		LeadSlug:    "ondrej",
		CompletedAt: time.Now().UTC(),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != LessonKindNegative {
		t.Errorf("FAILED → expected kind=negative, got %+v", entries)
	}
}

// TestEmitMissionOutcomeLesson_CancelledIsNeutral — CANCELLED isn't
// a failure (could be a re-scope, deprioritization) so it must land
// as neutral. Mixing it into the negative bucket would mislead the
// LEAD into reading every cancellation as something to avoid.
func TestEmitMissionOutcomeLesson_CancelledIsNeutral(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID:   "m_eng_99",
		Identifier:  "ENG-99",
		Title:       "Investigate flaky test",
		Status:      "CANCELLED",
		LeadSlug:    "tomas",
		CompletedAt: time.Now().UTC(),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != LessonKindNeutral {
		t.Errorf("CANCELLED → expected kind=neutral, got %+v", entries)
	}
}

// TestEmitMissionOutcomeLesson_NonTerminalIsNoop — only terminal
// states emit. Calling the helper on PLANNING / IN_PROGRESS / REVIEW
// must be a clean no-op (no error, no file written, no row added).
// This makes it safe to call unconditionally from a generic status
// transition handler.
func TestEmitMissionOutcomeLesson_NonTerminalIsNoop(t *testing.T) {
	for _, status := range []string{"PLANNING", "IN_PROGRESS", "REVIEW", "BACKLOG", "TODO"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			mo := MissionOutcome{
				MissionID:   "m_test",
				Identifier:  "T-1",
				Title:       "Test mission",
				Status:      status,
				LeadSlug:    "eva",
				CompletedAt: time.Now().UTC(),
			}
			if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
				t.Errorf("non-terminal status %s should not error: %v", status, err)
			}
			// File should not exist at all.
			if _, err := os.Stat(filepath.Join(dir, "lessons.md")); err == nil {
				t.Errorf("status=%s should not create lessons.md", status)
			}
		})
	}
}

// TestEmitMissionOutcomeLesson_IdempotentAcrossRetries — the mission
// hook runs after a tx commit, and a flaky filesystem (or operator
// re-running an export) could fire the helper twice for the same
// mission. The lesson ID is derived from mission ID, so re-runs must
// be no-ops on disk — the same single row, not duplicates.
func TestEmitMissionOutcomeLesson_IdempotentAcrossRetries(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID: "m_dup", Identifier: "DUP-1", Title: "duplicate test",
		Status: "COMPLETED", LeadSlug: "eva",
		CompletedAt: time.Date(2026, 5, 22, 14, 0, 0, 0, time.UTC),
	}
	for i := 0; i < 5; i++ {
		if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after 5 retries (idempotent), got %d", len(entries))
	}
}

// TestEmitMissionOutcomeLesson_EmptyMissionIDRejected — a missing
// mission_id makes the lesson ID degenerate to just
// "mission_outcome_" which would collide across missions. Boundary-
// validate this case before reaching the writer.
func TestEmitMissionOutcomeLesson_EmptyMissionIDRejected(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID:   "",
		Identifier:  "X-1",
		Title:       "no id",
		Status:      "COMPLETED",
		LeadSlug:    "eva",
		CompletedAt: time.Now().UTC(),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err == nil {
		t.Error("expected error for empty mission_id")
	}
}

// TestEmitMissionOutcomeLesson_ContextNoteCarriesLead — the context
// note is what the LEAD's boot prompt outcomes section renders next
// to each rule. It must include the LEAD slug and status so the LEAD
// reads "DEV-4 (failed, ondrej)" and can decide whether to follow up.
func TestEmitMissionOutcomeLesson_ContextNoteCarriesLead(t *testing.T) {
	dir := t.TempDir()
	mo := MissionOutcome{
		MissionID:   "m_x",
		Identifier:  "QUA-7",
		Title:       "review log parser PR",
		Status:      "COMPLETED",
		LeadSlug:    "beacon",
		CompletedAt: time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	}
	if err := EmitMissionOutcomeLesson(context.Background(), dir, mo); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !strings.Contains(entries[0].ContextNote, "beacon") {
		t.Errorf("context note must reference LEAD slug; got %q", entries[0].ContextNote)
	}
	if !strings.Contains(entries[0].ContextNote, "COMPLETED") {
		t.Errorf("context note must reference status; got %q", entries[0].ContextNote)
	}
}
