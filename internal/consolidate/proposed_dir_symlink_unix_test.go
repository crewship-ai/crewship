//go:build unix

package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func symlinkedProposedDir(t *testing.T) (outputDir, outsideDir string) {
	t.Helper()
	outputDir = t.TempDir()
	outsideDir = t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(outputDir, ".proposed")); err != nil {
		t.Fatalf("plant .proposed symlink: %v", err)
	}
	return outputDir, outsideDir
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory received %d entrie(s), want none", len(entries))
	}
}

func TestWriteProposalRefusesSymlinkedProposedDir(t *testing.T) {
	outputDir, outsideDir := symlinkedProposedDir(t)
	c := &Consolidator{Journal: &noopEmitter{}, Logger: quietLogger()}
	_, err := c.writeProposal(context.Background(), Config{
		WorkspaceID: "ws_test", CrewID: "crew_test", OutputDir: outputDir,
	}, time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC), []LearnedRule{{
		Pattern: "pattern", Action: "action",
	}}, 1)
	if err == nil {
		t.Fatal("writeProposal accepted a symlinked .proposed directory")
	}
	assertDirEmpty(t, outsideDir)
}

func TestPromoteRuleToSkillRefusesSymlinkedProposedDir(t *testing.T) {
	outputDir, outsideDir := symlinkedProposedDir(t)
	_, err := PromoteRuleToSkill(LearnedRule{
		Pattern: "safe pattern", Action: "safe action",
	}, ScoreResult{}, SkillPromoteOptions{OutputDir: outputDir})
	if err == nil {
		t.Fatal("PromoteRuleToSkill accepted a symlinked .proposed directory")
	}
	assertDirEmpty(t, outsideDir)
}
