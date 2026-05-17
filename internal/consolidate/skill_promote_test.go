package consolidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/skills"
)

// TestPromoteRuleToSkill_WritesParseableSKILLMD asserts the bridge writes a
// file that the canonical skills parser will accept. This is the contract
// that protects the rest of the skills pipeline (importer, UI, runtime
// injection) from auto-promoted memory rules that would later trip the
// validator.
func TestPromoteRuleToSkill_WritesParseableSKILLMD(t *testing.T) {
	tmp := t.TempDir()
	rule := LearnedRule{
		Pattern:    "user asks about Ultima Online Outlands shard rules",
		Action:     "search the official UO Outlands wiki at outlands.uooutlands.com before answering",
		Evidence:   []string{"j_001", "j_002", "j_003"},
		Confidence: 0.88,
	}
	score := ScoreResult{
		Composite:     0.91,
		RecallCount:   12,
		UniqueQueries: 5,
		Promoted:      true,
	}

	path, err := PromoteRuleToSkill(rule, score, SkillPromoteOptions{
		OutputDir: tmp,
		Now:       time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("PromoteRuleToSkill: %v", err)
	}
	if !strings.Contains(path, string(filepath.Separator)+".proposed"+string(filepath.Separator)) {
		t.Errorf("promoted skill should land under .proposed/, got %q", path)
	}
	if !strings.HasSuffix(filepath.Base(path), ".md") {
		t.Errorf("skill file should end in .md, got %q", filepath.Base(path))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read promoted file: %v", err)
	}
	parsed, err := skills.ParseSKILLMD(string(raw))
	if err != nil {
		t.Fatalf("canonical parser rejected our output: %v\nraw=\n%s", err, raw)
	}
	if parsed.Meta.Name == "" {
		t.Errorf("parser returned empty Name; frontmatter likely malformed")
	}
	// The promoted skill is by construction auto-generated, so we mark
	// it EXPERIMENTAL maturity — anything stronger would imply human
	// curation, which is exactly what HITL approval will add later.
	if parsed.Meta.Maturity != "EXPERIMENTAL" {
		t.Errorf("maturity = %q, want EXPERIMENTAL (auto-promoted skills are unvetted)", parsed.Meta.Maturity)
	}
	if parsed.Meta.Runtime != "INSTRUCTIONS" {
		t.Errorf("runtime = %q, want INSTRUCTIONS (no scripts/MCP for promoted rules)", parsed.Meta.Runtime)
	}
}

// TestPromoteRuleToSkill_DescriptionPassesLinter asserts the generated
// description contains a trigger phrase so the LLM router will actually
// match it. Skipping this check produces skills that load but never fire.
func TestPromoteRuleToSkill_DescriptionPassesLinter(t *testing.T) {
	tmp := t.TempDir()
	rule := LearnedRule{
		Pattern:    "deploying to production on Friday afternoon",
		Action:     "warn the user that the on-call rotation tightens after 16:00 UTC",
		Evidence:   []string{"j_a"},
		Confidence: 0.9,
	}
	path, err := PromoteRuleToSkill(rule, ScoreResult{Composite: 0.9, RecallCount: 11, Promoted: true},
		SkillPromoteOptions{OutputDir: tmp, Now: time.Now()})
	if err != nil {
		t.Fatalf("PromoteRuleToSkill: %v", err)
	}
	raw, _ := os.ReadFile(path)
	parsed, err := skills.ParseSKILLMD(string(raw))
	if err != nil {
		t.Fatalf("parser: %v", err)
	}
	if q := skills.LintDescription(parsed.Meta.Description); q != "" {
		t.Errorf("description fails linter: %s\ndescription=%q", q, parsed.Meta.Description)
	}
}

// TestPromoteRuleToSkill_EmbedsEvidence asserts every journal evidence ID
// appears in the body so a reviewing operator can trace the skill back to
// the events that justified it.
func TestPromoteRuleToSkill_EmbedsEvidence(t *testing.T) {
	tmp := t.TempDir()
	rule := LearnedRule{
		Pattern:    "ingesting CSV with BOM markers",
		Action:     "strip the BOM in the loader before passing to the parser",
		Evidence:   []string{"j_aaa111", "j_bbb222", "j_ccc333"},
		Confidence: 0.82,
	}
	path, err := PromoteRuleToSkill(rule, ScoreResult{Composite: 0.86, RecallCount: 10, Promoted: true},
		SkillPromoteOptions{OutputDir: tmp, Now: time.Now()})
	if err != nil {
		t.Fatalf("PromoteRuleToSkill: %v", err)
	}
	body, _ := os.ReadFile(path)
	for _, id := range rule.Evidence {
		if !strings.Contains(string(body), id) {
			t.Errorf("evidence id %q not embedded in skill body", id)
		}
	}
}

// TestPromoteEligibleRules_GatesOnRecallAndComposite asserts the bridge
// filters rules before promotion: a Promoted=true rule with recall<10 is
// rejected (the rule may have spiked in confidence but hasn't actually
// been useful), and a high-recall rule with composite<0.85 is rejected
// (recall alone without scoring confidence is a noise floor).
func TestPromoteEligibleRules_GatesOnRecallAndComposite(t *testing.T) {
	tmp := t.TempDir()
	rules := []LearnedRule{
		{Pattern: "high recall low score", Action: "do thing A", Evidence: []string{"e1"}},
		{Pattern: "low recall high score", Action: "do thing B", Evidence: []string{"e2"}},
		{Pattern: "passes both gates", Action: "do thing C", Evidence: []string{"e3"}},
	}
	scores := map[string]ScoreResult{
		"high recall low score": {Composite: 0.70, RecallCount: 20, Promoted: false},
		"low recall high score": {Composite: 0.92, RecallCount: 4, Promoted: true},
		"passes both gates":     {Composite: 0.88, RecallCount: 11, Promoted: true},
	}
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir:    tmp,
		Now:          time.Now(),
		MinRecall:    10,
		MinComposite: 0.85,
	})
	if err != nil {
		t.Fatalf("PromoteEligibleRules: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("want exactly 1 promoted skill, got %d: %v", len(written), written)
	}
	if !strings.Contains(written[0], "passes-both-gates") {
		t.Errorf("wrong rule promoted: %q", written[0])
	}
}

// TestPromoteRuleToSkill_DuplicateSlug_Disambiguates asserts running the
// bridge twice on the same rule does not clobber the prior staged
// proposal — operators may have already reviewed it. We append a numeric
// suffix instead.
func TestPromoteRuleToSkill_DuplicateSlug_Disambiguates(t *testing.T) {
	tmp := t.TempDir()
	rule := LearnedRule{
		Pattern:    "running migrations on Sunday",
		Action:     "block the merge if migrations changed in the last 24h",
		Evidence:   []string{"j_x"},
		Confidence: 0.9,
	}
	score := ScoreResult{Composite: 0.9, RecallCount: 11, Promoted: true}

	first, err := PromoteRuleToSkill(rule, score, SkillPromoteOptions{OutputDir: tmp, Now: time.Now()})
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	second, err := PromoteRuleToSkill(rule, score, SkillPromoteOptions{OutputDir: tmp, Now: time.Now()})
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if first == second {
		t.Errorf("second promotion clobbered the first: both at %q", first)
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("first proposal vanished: %v", err)
	}
}

// TestPromoteRuleToSkill_EmptyPattern_Errors guards against degenerate
// rules that would yield empty slugs (the skills.Slugify is lenient and
// would silently produce "" — promoting to a file literally named ".md"
// is worse than failing loudly).
func TestPromoteRuleToSkill_EmptyPattern_Errors(t *testing.T) {
	tmp := t.TempDir()
	_, err := PromoteRuleToSkill(
		LearnedRule{Pattern: "   ", Action: "x", Evidence: []string{"e"}},
		ScoreResult{Composite: 0.9, RecallCount: 11, Promoted: true},
		SkillPromoteOptions{OutputDir: tmp, Now: time.Now()},
	)
	if err == nil {
		t.Errorf("empty pattern should error, got nil")
	}
}
