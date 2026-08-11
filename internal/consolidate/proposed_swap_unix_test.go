//go:build unix

package consolidate

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func swapValidatedProposedDir(t *testing.T, outputDir, outsideDir string) func() {
	t.Helper()
	return func() {
		proposedDir := filepath.Join(outputDir, ".proposed")
		if err := os.Rename(proposedDir, proposedDir+"-original"); err != nil {
			t.Fatalf("rename validated .proposed: %v", err)
		}
		if err := os.Symlink(outsideDir, proposedDir); err != nil {
			t.Fatalf("swap .proposed to outside symlink: %v", err)
		}
	}
}

func assertNoProposedSwapWrites(t *testing.T, outsideDir string) {
	t.Helper()
	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("swapped outside directory received %d entrie(s): %v", len(entries), entries)
	}
}

func TestWriteProposalRefusesProposedDirSwapAfterValidation(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID:               "ws_test",
		CrewID:                    "crew_test",
		OutputDir:                 outputDir,
		afterProposedDirValidated: swapValidatedProposedDir(t, outputDir, outsideDir),
	}, time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err == nil {
		t.Error("writeProposal accepted a swapped .proposed directory")
	}
	assertNoProposedSwapWrites(t, outsideDir)
}

func TestPromoteRuleToSkillRefusesProposedDirSwapAfterValidation(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	_, err := PromoteRuleToSkill(LearnedRule{
		Pattern: "safe pattern", Action: "safe action",
	}, ScoreResult{}, SkillPromoteOptions{
		OutputDir:                 outputDir,
		Now:                       time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
		afterProposedDirValidated: swapValidatedProposedDir(t, outputDir, outsideDir),
	})
	if err == nil {
		t.Error("PromoteRuleToSkill accepted a swapped .proposed directory")
	}
	assertNoProposedSwapWrites(t, outsideDir)
}

func TestWriteProposalDoesNotFollowProposedDirSwapAfterRootOpen(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	result, err := c.writeProposal(context.Background(), Config{
		WorkspaceID:             "ws_test",
		CrewID:                  "crew_test",
		OutputDir:               outputDir,
		afterProposedRootOpened: swapValidatedProposedDir(t, outputDir, outsideDir),
	}, time.Date(2026, 8, 11, 10, 1, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err != nil {
		t.Fatalf("writeProposal after pathname swap: %v", err)
	}
	assertNoProposedSwapWrites(t, outsideDir)
	proposalName := filepath.Base(result.OutputPath)
	body, err := os.ReadFile(filepath.Join(outputDir, ".proposed-original", proposalName))
	if err != nil {
		t.Fatalf("read proposal through anchored directory: %v", err)
	}
	if !strings.Contains(string(body), "action") {
		t.Fatalf("anchored proposal does not contain rendered rule: %q", body)
	}
	if _, err := os.Stat(filepath.Join(outputDir, ".proposed-original", proposalName+".lock")); err != nil {
		t.Fatalf("stat lock through anchored directory: %v", err)
	}
}

func TestPromoteRuleToSkillDoesNotFollowProposedDirSwapAfterRootOpen(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	path, err := PromoteRuleToSkill(LearnedRule{
		Pattern: "safe pattern", Action: "safe action",
	}, ScoreResult{}, SkillPromoteOptions{
		OutputDir:               outputDir,
		Now:                     time.Date(2026, 8, 11, 10, 1, 0, 0, time.UTC),
		afterProposedRootOpened: swapValidatedProposedDir(t, outputDir, outsideDir),
	})
	if err != nil {
		t.Fatalf("PromoteRuleToSkill after pathname swap: %v", err)
	}
	assertNoProposedSwapWrites(t, outsideDir)
	body, err := os.ReadFile(filepath.Join(outputDir, ".proposed-original", filepath.Base(path)))
	if err != nil {
		t.Fatalf("read skill through anchored directory: %v", err)
	}
	if !strings.Contains(string(body), "safe action") {
		t.Fatalf("anchored skill does not contain rendered action: %q", body)
	}
}

func TestWriteProposalCleanupStaysAnchoredAfterRootOpen(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID:             "ws_test",
		CrewID:                  "crew_test",
		OutputDir:               outputDir,
		afterProposedRootOpened: swapValidatedProposedDir(t, outputDir, outsideDir),
	}, time.Date(2026, 8, 11, 10, 2, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action", Confidence: math.NaN(),
	}}, 1)
	if err == nil {
		t.Fatal("writeProposal accepted evidence that cannot be marshaled")
	}
	assertNoProposedSwapWrites(t, outsideDir)
	leftovers, globErr := filepath.Glob(filepath.Join(outputDir, ".proposed-original", "proposal-*.md"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leftovers) != 0 {
		t.Fatalf("anchored cleanup left proposal files behind: %v", leftovers)
	}
}

func TestWriteProposalDatabaseCleanupStaysAnchoredAfterRootOpen(t *testing.T) {
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	db := openDB(t) // Deliberately lacks memory_proposals, so the insert fails.
	defer db.Close()
	c := &Consolidator{DB: db, Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID:             "ws_test",
		CrewID:                  "crew_test",
		OutputDir:               outputDir,
		afterProposedRootOpened: swapValidatedProposedDir(t, outputDir, outsideDir),
	}, time.Date(2026, 8, 11, 10, 3, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err == nil || !strings.Contains(err.Error(), "insert memory_proposal") {
		t.Fatalf("writeProposal database failure = %v, want insert memory_proposal", err)
	}
	assertNoProposedSwapWrites(t, outsideDir)
	leftovers, globErr := filepath.Glob(filepath.Join(outputDir, ".proposed-original", "proposal-*.md"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(leftovers) != 0 {
		t.Fatalf("anchored database cleanup left proposal files behind: %v", leftovers)
	}
}
