package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestPromoteProposalSkills_PureBridge asserts the per-Consolidator hook
// calls PromoteEligibleRules with the default thresholds and returns the
// paths of any Skills that landed. The pure-function shape is the entry
// point the test suite uses to assert behavior without spinning up a
// full writeProposal — score injection at this layer is straightforward
// (a map literal) whereas through writeProposal it requires faking out
// the LLM extractor + the scoring CandidateMetrics population.
func TestPromoteProposalSkills_PureBridge(t *testing.T) {
	tmp := t.TempDir()
	c := &Consolidator{Logger: quietLogger()}

	rules := []LearnedRule{
		{Pattern: "high-recall stable rule", Action: "do canonical thing", Evidence: []string{"e1", "e2"}},
		{Pattern: "first-time noise", Action: "do thing once", Evidence: []string{"e3"}},
		{Pattern: "high recall but low score", Action: "do irrelevant", Evidence: []string{"e4"}},
	}
	scores := map[string]ScoreResult{
		"high-recall stable rule":   {Composite: 0.90, RecallCount: 14, Promoted: true},
		"first-time noise":          {Composite: 0.92, RecallCount: 1, Promoted: true},
		"high recall but low score": {Composite: 0.60, RecallCount: 30, Promoted: false},
	}

	paths := c.promoteProposalSkills(rules, scores, tmp, time.Now())
	if len(paths) != 1 {
		t.Fatalf("want exactly 1 promoted skill, got %d: %v", len(paths), paths)
	}
	if !strings.Contains(paths[0], "high-recall-stable-rule") {
		t.Errorf("wrong rule promoted: %q", paths[0])
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("staged skill file missing: %v", err)
	}
}

// TestWriteProposal_SkipsSkillStagingOnFirstRun asserts the wired path
// from writeProposal does NOT produce skill-*.md on a fresh proposal —
// the recall counter is 0 by construction at score time so the gate
// blocks. This is the expected steady-state: first proposals stage only
// proposal-*.md; skills appear on later runs once recall accumulates.
func TestWriteProposal_SkipsSkillStagingOnFirstRun(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	ids := seedEntries(t, db, w, "ws_x", "crew_y", 12, journal.EntryPeerEscalation)
	reply := `[{"pattern":"fresh","action":"a","evidence":["` + ids[0] + `","` + ids[1] + `"],"confidence":0.95}]`
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: reply}, Logger: quietLogger()}
	tmp := t.TempDir()
	cfg := Config{
		WorkspaceID: "ws_x", CrewID: "crew_y", Since: time.Hour,
		MinEntries: 10, OutputDir: tmp, ProposalMode: true,
	}
	res, err := c.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Skipped {
		t.Fatalf("Run skipped: entries=%d, rules=%d, OutputPath=%q", res.EntriesScanned, res.RulesAppended, res.OutputPath)
	}
	// .proposed/proposal-*.md must exist, but skill-*.md must NOT —
	// recall=0 on a brand-new rule is the gate's whole point.
	entries, err := os.ReadDir(filepath.Join(tmp, ".proposed"))
	if err != nil {
		t.Fatalf("read .proposed: %v (Run result: %+v)", err, res)
	}
	var sawProposal, sawSkill bool
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "proposal-"):
			sawProposal = true
		case strings.HasPrefix(name, "skill-"):
			sawSkill = true
		}
	}
	if !sawProposal {
		t.Errorf("expected proposal-*.md in .proposed/")
	}
	if sawSkill {
		t.Errorf("first-run proposal must not produce skill-*.md (recall=0); listing=%v", entries)
	}
}
