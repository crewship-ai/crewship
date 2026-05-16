package consolidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDedupRules_NoPriorFiles_AllPass: with no prior learned-*.md the
// dedup pass is a no-op.
func TestDedupRules_NoPriorFiles_AllPass(t *testing.T) {
	dir := t.TempDir()
	in := []LearnedRule{
		{Pattern: "X happens", Action: "do Y", Confidence: 0.8, Evidence: []string{"e1"}},
		{Pattern: "Z happens", Action: "do W", Confidence: 0.7, Evidence: []string{"e2"}},
	}
	out := dedupAgainstPrior(in, dir, time.Now(), 7*24*time.Hour)
	if len(out) != 2 {
		t.Fatalf("want 2 rules, got %d", len(out))
	}
}

// TestDedupRules_ExactPatternInRecent_Dropped asserts a rule whose
// pattern hash matches one already in a recent learned-*.md is
// dropped from the candidate list.
func TestDedupRules_ExactPatternInRecent_Dropped(t *testing.T) {
	dir := t.TempDir()
	yesterday := time.Now().Add(-24 * time.Hour)
	prior := "## Run at 03:00 UTC\n\n" +
		"- **Pattern:** X happens  \n" +
		"  **Action:** do Y  \n" +
		"  **Confidence:** 0.80  \n" +
		"  **Evidence:** e_old\n"
	priorPath := filepath.Join(dir, "learned-"+yesterday.Format("2006-01-02")+".md")
	if err := os.WriteFile(priorPath, []byte(prior), 0o644); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	in := []LearnedRule{
		{Pattern: "X happens", Action: "do Y", Confidence: 0.8},
		{Pattern: "Z happens", Action: "do W", Confidence: 0.7},
	}
	out := dedupAgainstPrior(in, dir, time.Now(), 7*24*time.Hour)
	if len(out) != 1 {
		t.Fatalf("want 1 rule after dedup, got %d (%+v)", len(out), out)
	}
	if out[0].Pattern != "Z happens" {
		t.Errorf("expected Z happens to survive, got %q", out[0].Pattern)
	}
}

// TestDedupRules_NormalisedComparison: trailing whitespace, leading
// dash, and case differences in the pattern should not defeat dedup.
func TestDedupRules_NormalisedComparison(t *testing.T) {
	dir := t.TempDir()
	yesterday := time.Now().Add(-24 * time.Hour)
	prior := "- **Pattern:** X Happens  \n  **Action:** do Y\n"
	priorPath := filepath.Join(dir, "learned-"+yesterday.Format("2006-01-02")+".md")
	if err := os.WriteFile(priorPath, []byte(prior), 0o644); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	in := []LearnedRule{
		{Pattern: "  x happens  ", Action: "do Y"},
	}
	out := dedupAgainstPrior(in, dir, time.Now(), 7*24*time.Hour)
	if len(out) != 0 {
		t.Errorf("normalised match should have dropped rule, got %d remaining", len(out))
	}
}

// TestDedupRules_OutsideWindow_NotApplied: a pattern that lives in a
// file older than the dedup window should not block a fresh candidate.
func TestDedupRules_OutsideWindow_NotApplied(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-30 * 24 * time.Hour)
	priorPath := filepath.Join(dir, "learned-"+old.Format("2006-01-02")+".md")
	if err := os.WriteFile(priorPath, []byte("- **Pattern:** X happens\n"), 0o644); err != nil {
		t.Fatalf("seed prior: %v", err)
	}

	in := []LearnedRule{{Pattern: "X happens", Action: "Y"}}
	out := dedupAgainstPrior(in, dir, time.Now(), 7*24*time.Hour)
	if len(out) != 1 {
		t.Errorf("rule outside 7-day window should survive, got %d (%+v)", len(out), out)
	}
}

// TestDedupRules_NoOutputDir: a non-existent dir is a no-op.
func TestDedupRules_NoOutputDir(t *testing.T) {
	in := []LearnedRule{{Pattern: "X", Action: "Y"}}
	out := dedupAgainstPrior(in, "/definitely/does/not/exist", time.Now(), 7*24*time.Hour)
	if len(out) != 1 {
		t.Errorf("dedup with missing dir should pass through, got %d", len(out))
	}
}

// TestDedupRules_BadFiles_Skipped: unreadable / non-markdown junk in
// the dir does not crash dedup; rule passes through.
func TestDedupRules_BadFiles_Skipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatalf("seed junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "learned-not-a-date.md"), []byte("- **Pattern:** X\n"), 0o644); err != nil {
		t.Fatalf("seed bad filename: %v", err)
	}
	in := []LearnedRule{{Pattern: "X", Action: "Y"}}
	out := dedupAgainstPrior(in, dir, time.Now(), 7*24*time.Hour)
	// Bad filename means the date couldn't be parsed; we conservatively
	// skip rather than scan it.
	if len(out) != 1 {
		t.Errorf("malformed filename should not block dedup pass-through, got %d (%+v)", len(out), out)
	}
	_ = strings.Builder{} // appease unused-import linter when removing later
}
