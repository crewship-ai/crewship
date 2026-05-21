package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWriteLesson_CreatesFile_WithFrontmatter is the foundational TDD
// assertion for PR-Z Z.7: a first-write call creates a lessons.md
// file under the agent's memory dir, populated with YAML frontmatter
// and a single entry block. The frontmatter must include kind so
// downstream readers can filter positive vs negative vs neutral
// lessons without parsing the body. PR-C F4.4 will be the first
// real consumer; this writer is the stable primitive it imports.
func TestWriteLesson_CreatesFile_WithFrontmatter(t *testing.T) {
	dir := t.TempDir()
	entry := LessonEntry{
		ID:          "ent_pos_001",
		Kind:        LessonKindPositive,
		CapturedAt:  time.Date(2026, 5, 20, 23, 50, 0, 0, time.UTC),
		Source:      LessonSourceManual,
		Rule:        "Run `pnpm test:watch` before commit to catch snapshot regressions",
		ContextNote: "Caught 4 missing snapshots that would have broken CI",
	}

	if err := WriteLesson(context.Background(), dir, entry); err != nil {
		t.Fatalf("WriteLesson failed: %v", err)
	}

	path := filepath.Join(dir, "lessons.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected lessons.md at %s: %v", path, err)
	}
	got := string(data)
	for _, want := range []string{
		"id: ent_pos_001",
		"kind: positive",
		"captured_at: 2026-05-20T23:50:00Z",
		"source: manual",
		"Run `pnpm test:watch` before commit",
		"Caught 4 missing snapshots",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lessons.md missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestWriteLesson_AppendsSecondEntry verifies that the writer appends
// new entries without rewriting existing ones — lessons.md is an
// append-mostly log keyed by entry ID. Idempotency via ID means a
// retried write (replay, hook double-fire) doesn't duplicate rows.
func TestWriteLesson_AppendsSecondEntry(t *testing.T) {
	dir := t.TempDir()
	first := LessonEntry{
		ID:         "ent_a",
		Kind:       LessonKindPositive,
		CapturedAt: time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		Source:     LessonSourceSkillPromote,
		Rule:       "first lesson",
	}
	second := LessonEntry{
		ID:         "ent_b",
		Kind:       LessonKindNegative,
		CapturedAt: time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC),
		Source:     LessonSourceNegativeLearning,
		Rule:       "second lesson, this one negative",
	}
	if err := WriteLesson(context.Background(), dir, first); err != nil {
		t.Fatal(err)
	}
	if err := WriteLesson(context.Background(), dir, second); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)

	if !strings.Contains(body, "id: ent_a") || !strings.Contains(body, "id: ent_b") {
		t.Errorf("both entries must appear; got:\n%s", body)
	}
	if !strings.Contains(body, "kind: positive") || !strings.Contains(body, "kind: negative") {
		t.Errorf("both kinds must appear; got:\n%s", body)
	}
}

// TestWriteLesson_IdempotentByID re-runs the SAME entry twice (same
// ID) and asserts only one block is persisted. Idempotency is the
// expected contract for replay-safe write paths — F4.4 will fire on
// every guardrail-trigger and we can't have the same antipattern
// recorded N times because the dispatcher retried.
func TestWriteLesson_IdempotentByID(t *testing.T) {
	dir := t.TempDir()
	entry := LessonEntry{
		ID:         "ent_dup",
		Kind:       LessonKindNeutral,
		CapturedAt: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC),
		Source:     LessonSourceManual,
		Rule:       "same rule",
	}
	for i := 0; i < 3; i++ {
		if err := WriteLesson(context.Background(), dir, entry); err != nil {
			t.Fatalf("attempt %d failed: %v", i, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "lessons.md"))
	if err != nil {
		t.Fatalf("read lessons.md: %v", err)
	}
	body := string(data)
	count := strings.Count(body, "id: ent_dup")
	if count != 1 {
		t.Errorf("expected exactly 1 block for ent_dup, got %d\n%s", count, body)
	}
}

// TestWriteLesson_RejectsInvalidKind blocks bad-kind writes at the
// boundary so consumers can't accidentally write `kind: bogus` and
// have downstream filters silently exclude them.
func TestWriteLesson_RejectsInvalidKind(t *testing.T) {
	dir := t.TempDir()
	bad := LessonEntry{
		ID:         "ent_x",
		Kind:       "bogus",
		CapturedAt: time.Now().UTC(),
		Source:     LessonSourceManual,
		Rule:       "shouldn't land",
	}
	if err := WriteLesson(context.Background(), dir, bad); err == nil {
		t.Fatal("expected error for invalid kind 'bogus'")
	}
}

// TestWriteLesson_RejectsEmptyID blocks empty-ID writes — ID is the
// idempotency key; without it the dedup contract collapses.
func TestWriteLesson_RejectsEmptyID(t *testing.T) {
	dir := t.TempDir()
	bad := LessonEntry{
		Kind:       LessonKindPositive,
		CapturedAt: time.Now().UTC(),
		Source:     LessonSourceManual,
		Rule:       "needs id",
	}
	if err := WriteLesson(context.Background(), dir, bad); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

// TestWriteLesson_RejectsEmptyRule blocks empty-rule writes — a
// lesson with no rule body is noise.
func TestWriteLesson_RejectsEmptyRule(t *testing.T) {
	dir := t.TempDir()
	bad := LessonEntry{
		ID:         "ent_y",
		Kind:       LessonKindPositive,
		CapturedAt: time.Now().UTC(),
		Source:     LessonSourceManual,
		Rule:       "",
	}
	if err := WriteLesson(context.Background(), dir, bad); err == nil {
		t.Fatal("expected error for empty rule")
	}
}

// TestReadLessons_FiltersByKind reads back lessons via the companion
// read helper and asserts kind filtering works — this is how F4.4 +
// the UI LessonsLearnedCard widget pull subsets without parsing
// every entry.
func TestReadLessons_FiltersByKind(t *testing.T) {
	dir := t.TempDir()
	entries := []LessonEntry{
		{ID: "p1", Kind: LessonKindPositive, CapturedAt: time.Now().UTC(), Source: LessonSourceManual, Rule: "pos 1"},
		{ID: "n1", Kind: LessonKindNegative, CapturedAt: time.Now().UTC(), Source: LessonSourceNegativeLearning, Rule: "neg 1"},
		{ID: "p2", Kind: LessonKindPositive, CapturedAt: time.Now().UTC(), Source: LessonSourceSkillPromote, Rule: "pos 2"},
	}
	for _, e := range entries {
		if err := WriteLesson(context.Background(), dir, e); err != nil {
			t.Fatal(err)
		}
	}

	pos, err := ReadLessons(context.Background(), dir, LessonKindPositive)
	if err != nil {
		t.Fatal(err)
	}
	if len(pos) != 2 {
		t.Errorf("expected 2 positive lessons, got %d", len(pos))
	}

	neg, err := ReadLessons(context.Background(), dir, LessonKindNegative)
	if err != nil {
		t.Fatal(err)
	}
	if len(neg) != 1 {
		t.Errorf("expected 1 negative lesson, got %d", len(neg))
	}

	all, err := ReadLessons(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 total lessons with empty filter, got %d", len(all))
	}
}
