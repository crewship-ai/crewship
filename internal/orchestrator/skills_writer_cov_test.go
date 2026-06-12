package orchestrator

// Coverage tests for skills_writer.go: pruneStaleSkillFolders (orphan
// removal, unsafe-name filtering, cursor .mdc handling, error tolerance)
// and writeAgentSkills failure/skip paths. Uses a scripted covContainer
// that answers `ls -1 <root>` listings and records every rm command.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covSkillsContainer builds a covContainer that serves directory listings
// (script prefix "ls -1 <root>") from the listings map and optionally fails
// rm / write scripts.
func covSkillsContainer(listings map[string]string, failRM, failWrites, failLS bool) *covContainer {
	return &covContainer{route: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		script := covScript(cfg)
		if strings.Contains(script, "ls -1 ") {
			if failLS {
				return nil, errors.New("ls broken")
			}
			for root, listing := range listings {
				if strings.Contains(script, "ls -1 "+root+" ") {
					return covResult("ls", listing), nil
				}
			}
			return covResult("ls", ""), nil
		}
		if strings.HasPrefix(script, "sh -c rm -rf ") || strings.HasPrefix(script, "sh -c rm -f ") {
			if failRM {
				return nil, errors.New("rm broken")
			}
			return nil, nil
		}
		if strings.Contains(script, "base64 -d >") && failWrites {
			return nil, errors.New("write broken")
		}
		return nil, nil
	}}
}

func covRMCommands(c *covContainer) []string {
	var out []string
	for _, call := range c.snapshotCalls() {
		if len(call.Cmd) == 3 && call.Cmd[0] == "sh" && strings.HasPrefix(call.Cmd[2], "rm ") {
			out = append(out, call.Cmd[2])
		}
	}
	return out
}

func TestPruneStaleSkillFolders_RemovesOrphansKeepsLive(t *testing.T) {
	t.Parallel()
	listings := map[string]string{
		".claude/skills":   "alpha\nstale-one\nBad Name\n..\n",
		".agents/skills":   "",
		".opencode/skills": "",
		".factory/skills":  "",
		".cursor/rules":    "stale.mdc\nalpha.mdc\nREADME.txt\nbad name.mdc\n",
	}
	c := covSkillsContainer(listings, false, false, false)
	keep := map[string]struct{}{"alpha": {}}
	pruneStaleSkillFolders(context.Background(), c, "ctr1", "/output/bob",
		[]string{".claude/skills", ".agents/skills", ".opencode/skills", ".factory/skills"},
		keep, covQuietLogger())

	rms := covRMCommands(c)
	if len(rms) != 2 {
		t.Fatalf("expected 2 rm commands (folders + cursor rules), got %d: %v", len(rms), rms)
	}
	var folderRM, cursorRM string
	for _, rm := range rms {
		if strings.HasPrefix(rm, "rm -rf ") {
			folderRM = rm
		}
		if strings.HasPrefix(rm, "rm -f ") {
			cursorRM = rm
		}
	}
	if !strings.Contains(folderRM, ".claude/skills/stale-one") {
		t.Errorf("orphan folder not removed: %q", folderRM)
	}
	if strings.Contains(folderRM, "alpha") || strings.Contains(folderRM, "Bad Name") {
		t.Errorf("kept/unsafe entries must not be removed: %q", folderRM)
	}
	if !strings.Contains(cursorRM, ".cursor/rules/stale.mdc") {
		t.Errorf("orphan .mdc not removed: %q", cursorRM)
	}
	if strings.Contains(cursorRM, "alpha.mdc") || strings.Contains(cursorRM, "README.txt") || strings.Contains(cursorRM, "bad name.mdc") {
		t.Errorf("kept / non-mdc / unsafe entries must not be removed: %q", cursorRM)
	}
}

func TestPruneStaleSkillFolders_NoOrphansNoRM(t *testing.T) {
	t.Parallel()
	listings := map[string]string{
		".claude/skills": "alpha\n",
		".cursor/rules":  "alpha.mdc\n",
	}
	c := covSkillsContainer(listings, false, false, false)
	pruneStaleSkillFolders(context.Background(), c, "ctr1", "/output/bob",
		[]string{".claude/skills"}, map[string]struct{}{"alpha": {}}, covQuietLogger())
	if rms := covRMCommands(c); len(rms) != 0 {
		t.Errorf("nothing should be removed, got %v", rms)
	}
}

func TestPruneStaleSkillFolders_ListAndRMFailuresTolerated(t *testing.T) {
	t.Parallel()
	// ls failures: warn + continue, never panic.
	cLS := covSkillsContainer(nil, false, false, true)
	pruneStaleSkillFolders(context.Background(), cLS, "ctr1", "/output/bob",
		[]string{".claude/skills"}, map[string]struct{}{}, covQuietLogger())
	if rms := covRMCommands(cLS); len(rms) != 0 {
		t.Errorf("no rm expected when ls fails, got %v", rms)
	}

	// rm failures: warn + continue.
	listings := map[string]string{
		".claude/skills": "orphan\n",
		".cursor/rules":  "orphan.mdc\n",
	}
	cRM := covSkillsContainer(listings, true, false, false)
	pruneStaleSkillFolders(context.Background(), cRM, "ctr1", "/output/bob",
		[]string{".claude/skills"}, map[string]struct{}{}, covQuietLogger()) // must not panic
}

func TestWriteAgentSkills_EmptySkillsPrunesThenReturnsNil(t *testing.T) {
	t.Parallel()
	listings := map[string]string{".claude/skills": "orphan\n"}
	c := covSkillsContainer(listings, false, false, false)
	if err := writeAgentSkills(context.Background(), c, "ctr1", "/output/bob", nil, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rms := covRMCommands(c)
	if len(rms) == 0 || !strings.Contains(rms[0], ".claude/skills/orphan") {
		t.Errorf("prune must still run for empty skill set, got %v", rms)
	}
}

func TestWriteAgentSkills_WritesAllDiscoveryPaths(t *testing.T) {
	t.Parallel()
	c := covSkillsContainer(nil, false, false, false)
	skills := []SkillBundle{
		{Slug: "review-pr", Content: "---\nname: review-pr\n---\nbody"},
		{Slug: "", Content: "skipped-no-slug"},
		{Slug: "valid-but-empty", Content: ""},
		{Slug: "../escape", Content: "evil"},
	}
	if err := writeAgentSkills(context.Background(), c, "ctr1", "/output/bob", skills, covQuietLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var writtenPaths []string
	for _, call := range c.snapshotCalls() {
		script := covScript(call)
		if strings.Contains(script, "base64 -d >") {
			writtenPaths = append(writtenPaths, script)
		}
	}
	wantFragments := []string{
		".claude/skills/review-pr/SKILL.md",
		".agents/skills/review-pr/SKILL.md",
		".opencode/skills/review-pr/SKILL.md",
		".factory/skills/review-pr/SKILL.md",
		".cursor/rules/review-pr.mdc",
	}
	joined := strings.Join(writtenPaths, "\n")
	for _, frag := range wantFragments {
		if !strings.Contains(joined, frag) {
			t.Errorf("missing write to %s in:\n%s", frag, joined)
		}
	}
	if len(writtenPaths) != len(wantFragments) {
		t.Errorf("want exactly %d writes (rejected slugs skipped), got %d", len(wantFragments), len(writtenPaths))
	}
	if strings.Contains(joined, "escape") {
		t.Errorf("path-traversal slug must never reach a write: %s", joined)
	}
}

func TestWriteAgentSkills_AllWritesFailingReturnsError(t *testing.T) {
	t.Parallel()
	c := covSkillsContainer(nil, false, true, false)
	skills := []SkillBundle{{Slug: "doomed", Content: "body"}}
	err := writeAgentSkills(context.Background(), c, "ctr1", "/output/bob", skills, covQuietLogger())
	if err == nil || !strings.Contains(err.Error(), "zero of 1 skills landed") {
		t.Fatalf("expected zero-landed error, got %v", err)
	}
	if !strings.Contains(err.Error(), "write broken") {
		t.Errorf("error must wrap the first concrete write failure: %v", err)
	}
}
