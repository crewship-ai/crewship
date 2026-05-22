package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLessonSourceMissionOutcome_AcceptedByWriter is the gate that
// the mission-outcomes-to-memory PRD relies on. The new
// LessonSourceMissionOutcome must be in validLessonSources or the
// writer rejects every mission-completion hook call before it gets
// to disk. A typo'd const (e.g. "missionoutcome") would silently
// fall through to the "invalid source" path without this test.
func TestLessonSourceMissionOutcome_AcceptedByWriter(t *testing.T) {
	dir := t.TempDir()
	entry := LessonEntry{
		ID:         "mission_outcome_ENG-1",
		Kind:       LessonKindPositive,
		CapturedAt: time.Date(2026, 5, 22, 14, 30, 0, 0, time.UTC),
		Source:     LessonSourceMissionOutcome,
		Rule:       "ENG-1 completed: ping google.com 5 times — chose Bash over Python",
	}
	if err := WriteLesson(context.Background(), dir, entry); err != nil {
		t.Fatalf("WriteLesson with LessonSourceMissionOutcome failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "lessons.md"))
	if err != nil {
		t.Fatalf("read lessons.md: %v", err)
	}
	if !strings.Contains(string(data), "source: mission_outcome") {
		t.Errorf("lessons.md must include 'source: mission_outcome'; got:\n%s", string(data))
	}
}

// TestWriteCrewLesson_CreatesFile_InCrewSharedDir verifies that the
// crew-tier writer lands lessons.md under the crew-shared dir (not
// the per-agent dir). The two paths are independent so an agent
// writing its private lesson should never collide with the
// crew-shared outcomes log.
func TestWriteCrewLesson_CreatesFile_InCrewSharedDir(t *testing.T) {
	root := t.TempDir()
	crewSharedDir := filepath.Join(root, "crews", "abc123", "shared", ".memory")
	entry := LessonEntry{
		ID:         "mission_outcome_QUA-3",
		Kind:       LessonKindPositive,
		CapturedAt: time.Date(2026, 5, 22, 15, 0, 0, 0, time.UTC),
		Source:     LessonSourceMissionOutcome,
		Rule:       "QUA-3 completed: log parser caught errors via grep -E pattern",
	}
	if err := WriteCrewLesson(context.Background(), crewSharedDir, entry); err != nil {
		t.Fatalf("WriteCrewLesson failed: %v", err)
	}
	path := filepath.Join(crewSharedDir, "lessons.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected crew lessons.md at %s: %v", path, err)
	}
	for _, want := range []string{
		"id: mission_outcome_QUA-3",
		"kind: positive",
		"source: mission_outcome",
		"QUA-3 completed",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("crew lessons.md missing %q\n--- got ---\n%s", want, string(data))
		}
	}
}

// TestWriteCrewLesson_IdempotentByID re-runs the same outcome twice
// (mission status hook double-fires across retries) and asserts the
// file ends with one block, not two. Mirrors the per-agent writer's
// contract — replay-safety is the entire point of the ID field.
func TestWriteCrewLesson_IdempotentByID(t *testing.T) {
	dir := t.TempDir()
	entry := LessonEntry{
		ID:         "mission_outcome_DEV-4",
		Kind:       LessonKindNegative,
		CapturedAt: time.Date(2026, 5, 22, 16, 0, 0, 0, time.UTC),
		Source:     LessonSourceMissionOutcome,
		Rule:       "DEV-4 failed: network probe required sudo not available in container",
	}
	for i := 0; i < 4; i++ {
		if err := WriteCrewLesson(context.Background(), dir, entry); err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), "id: mission_outcome_DEV-4")
	if count != 1 {
		t.Errorf("expected exactly 1 block, got %d:\n%s", count, string(data))
	}
}

// TestWriteCrewLesson_AppendsMultipleOutcomes — a healthy crew accumulates
// multiple mission outcomes; the writer must preserve all of them in
// capture order without merging or dropping any.
func TestWriteCrewLesson_AppendsMultipleOutcomes(t *testing.T) {
	dir := t.TempDir()
	outcomes := []LessonEntry{
		{
			ID: "mission_outcome_ENG-1", Kind: LessonKindPositive,
			CapturedAt: time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
			Source:     LessonSourceMissionOutcome, Rule: "ENG-1 completed",
		},
		{
			ID: "mission_outcome_ENG-2", Kind: LessonKindNegative,
			CapturedAt: time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
			Source:     LessonSourceMissionOutcome, Rule: "ENG-2 failed",
		},
		{
			ID: "mission_outcome_ENG-3", Kind: LessonKindNeutral,
			CapturedAt: time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
			Source:     LessonSourceMissionOutcome, Rule: "ENG-3 cancelled",
		},
	}
	for _, e := range outcomes {
		if err := WriteCrewLesson(context.Background(), dir, e); err != nil {
			t.Fatalf("WriteCrewLesson %s: %v", e.ID, err)
		}
	}
	entries, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for i, want := range []string{"mission_outcome_ENG-1", "mission_outcome_ENG-2", "mission_outcome_ENG-3"} {
		if entries[i].ID != want {
			t.Errorf("entry[%d]: want id %q, got %q", i, want, entries[i].ID)
		}
	}
}

// TestWriteCrewLesson_RejectsInvalidInputs — boundary validation must
// mirror WriteLesson so a buggy hook can't land malformed entries
// in the crew tier either.
func TestWriteCrewLesson_RejectsInvalidInputs(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name  string
		entry LessonEntry
	}{
		{"empty_id", LessonEntry{
			Kind: LessonKindPositive, CapturedAt: now,
			Source: LessonSourceMissionOutcome, Rule: "needs id",
		}},
		{"empty_rule", LessonEntry{
			ID: "mission_outcome_x", Kind: LessonKindPositive, CapturedAt: now,
			Source: LessonSourceMissionOutcome, Rule: "",
		}},
		{"invalid_kind", LessonEntry{
			ID: "mission_outcome_y", Kind: "bogus", CapturedAt: now,
			Source: LessonSourceMissionOutcome, Rule: "shouldn't land",
		}},
		{"invalid_source", LessonEntry{
			ID: "mission_outcome_z", Kind: LessonKindPositive, CapturedAt: now,
			Source: "fictional_source", Rule: "rejected",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteCrewLesson(context.Background(), dir, tc.entry); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestWriteCrewLesson_RejectsPathTraversal — boundary defense:
// reject empty / root / "." / "./" inputs, and any relative path
// that filepath.Clean cannot collapse without leaving a ".." segment.
//
// Note on scope: this test pins the writer-layer guards only. Absolute
// paths whose ".." segments cleanly resolve (e.g. /a/b/../c → /a/c)
// are NOT rejected here — that's by design, because Clean has already
// turned them into a safe absolute path. The mission-outcome hook's
// safeIDPattern (in internal/api) is what prevents a hostile or
// corrupted crew_id from contributing the ".." in the first place.
func TestWriteCrewLesson_RejectsPathTraversal(t *testing.T) {
	now := time.Now().UTC()
	good := LessonEntry{
		ID: "ok", Kind: LessonKindPositive, CapturedAt: now,
		Source: LessonSourceMissionOutcome, Rule: "rule",
	}
	cases := []struct {
		name string
		dir  string
	}{
		{"empty_dir", ""},
		{"root_dir", "/"},
		{"dot_dir", "."},
		{"relative_dotdot_unclean", "../escaped/.memory"}, // Clean leaves ".."
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := WriteCrewLesson(context.Background(), tc.dir, good); err == nil {
				t.Fatalf("expected error for %s (dir=%q), got nil", tc.name, tc.dir)
			}
		})
	}
}

// TestWriteCrewLesson_IsolatedFromAgentLessons — writes to the crew
// shared dir must not bleed into per-agent dirs and vice-versa.
// Sibling paths under the same root must keep separate lessons.md
// contents.
func TestWriteCrewLesson_IsolatedFromAgentLessons(t *testing.T) {
	root := t.TempDir()
	crewDir := filepath.Join(root, "crew_shared")
	agentDir := filepath.Join(root, "agent_filip")

	crewEntry := LessonEntry{
		ID: "crew_lesson", Kind: LessonKindPositive,
		CapturedAt: time.Now().UTC(),
		Source:     LessonSourceMissionOutcome, Rule: "crew-wide",
	}
	agentEntry := LessonEntry{
		ID: "agent_lesson", Kind: LessonKindPositive,
		CapturedAt: time.Now().UTC(),
		Source:     LessonSourceManual, Rule: "personal",
	}
	if err := WriteCrewLesson(context.Background(), crewDir, crewEntry); err != nil {
		t.Fatal(err)
	}
	if err := WriteLesson(context.Background(), agentDir, agentEntry); err != nil {
		t.Fatal(err)
	}

	crewData, err := os.ReadFile(filepath.Join(crewDir, "lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	agentData, err := os.ReadFile(filepath.Join(agentDir, "lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(crewData), "agent_lesson") {
		t.Errorf("crew lessons.md leaked agent_lesson; got:\n%s", string(crewData))
	}
	if strings.Contains(string(agentData), "crew_lesson") {
		t.Errorf("agent lessons.md leaked crew_lesson; got:\n%s", string(agentData))
	}
}
