package consolidate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- PromoteRuleToSkill error/edge paths -----------------------------------------

func TestPromoteRuleToSkill_InputValidation(t *testing.T) {
	score := ScoreResult{Composite: 0.9, RecallCount: 12, UniqueQueries: 5}

	// Empty pattern.
	if _, err := PromoteRuleToSkill(LearnedRule{Pattern: "   "}, score,
		SkillPromoteOptions{OutputDir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "pattern is empty") {
		t.Errorf("blank pattern: got %v", err)
	}

	// Missing OutputDir.
	if _, err := PromoteRuleToSkill(LearnedRule{Pattern: "p"}, score,
		SkillPromoteOptions{}); err == nil || !strings.Contains(err.Error(), "OutputDir is required") {
		t.Errorf("missing OutputDir: got %v", err)
	}

	// Pattern that slugifies to nothing.
	if _, err := PromoteRuleToSkill(LearnedRule{Pattern: "!!! ???"}, score,
		SkillPromoteOptions{OutputDir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "slugified to empty") {
		t.Errorf("empty slug: got %v", err)
	}
}

func TestPromoteRuleToSkill_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("blocker: %v", err)
	}
	_, err := PromoteRuleToSkill(LearnedRule{Pattern: "valid pattern"},
		ScoreResult{}, SkillPromoteOptions{OutputDir: blocker})
	if err == nil || !strings.Contains(err.Error(), "mkdir .proposed") {
		t.Errorf("expected mkdir error, got %v", err)
	}
}

func TestPromoteRuleToSkill_ZeroNowDefaultsAndWritesValidDoc(t *testing.T) {
	dir := t.TempDir()
	rule := LearnedRule{
		Pattern:  "Deploys fail on Friday: rollback window too short",
		Action:   "Schedule deploys before Thursday 16:00 UTC",
		Evidence: []string{"j_1", "j_2"},
	}
	score := ScoreResult{Composite: 0.91, RecallCount: 14, UniqueQueries: 6}

	path, err := PromoteRuleToSkill(rule, score, SkillPromoteOptions{OutputDir: dir}) // Now zero → time.Now()
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(dir, ".proposed") {
		t.Errorf("staged outside .proposed/: %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		"name: deploys-fail-on-friday",
		`description: "Use when`, // safeYAMLString-quoted description
		"category: CUSTOM",
		"runtime: INSTRUCTIONS",
		"maturity: EXPERIMENTAL",
		"author: crewship-consolidator",
		"- auto-promoted",
		"- memory-derived",
		"14 recall events across 6 distinct queries with composite score 0.91",
		"- `j_1`",
		"- `j_2`",
		// "auto-promoted on <today>" — Now defaulted to wall clock.
		"auto-promoted on " + time.Now().UTC().Format("2006-01-02"),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md missing %q:\n%s", want, s)
		}
	}
	// The pattern's ':' must be inside the quoted description, never a bare
	// YAML key/value split.
	if strings.Contains(s, "description: Use when") {
		t.Errorf("description not safe-quoted:\n%s", s)
	}
}

// --- writeUniqueSkillFile ------------------------------------------------------------

func TestWriteUniqueSkillFile_SuffixesOnCollision(t *testing.T) {
	dir := t.TempDir()
	var got []string
	for i := 0; i < 3; i++ {
		p, err := writeUniqueSkillFile(dir, "dup", []byte(fmt.Sprintf("body %d", i)))
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got = append(got, filepath.Base(p))
	}
	want := []string{"skill-dup.md", "skill-dup-2.md", "skill-dup-3.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}
	// Contents must NOT be clobbered — each file keeps its own body.
	b, _ := os.ReadFile(filepath.Join(dir, "skill-dup.md"))
	if string(b) != "body 0" {
		t.Errorf("original file clobbered: %q", b)
	}
}

func TestWriteUniqueSkillFile_DirMissing(t *testing.T) {
	_, err := writeUniqueSkillFile(filepath.Join(t.TempDir(), "nope"), "x", []byte("b"))
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Errorf("expected open error, got %v", err)
	}
}

func TestWriteUniqueSkillFile_ExhaustsSuffixes(t *testing.T) {
	dir := t.TempDir()
	// Pre-create every candidate name (i=1..99).
	for i := 1; i < 100; i++ {
		name := "skill-full.md"
		if i > 1 {
			name = fmt.Sprintf("skill-full-%d.md", i)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("taken"), 0o644); err != nil {
			t.Fatalf("pre-create %s: %v", name, err)
		}
	}
	_, err := writeUniqueSkillFile(dir, "full", []byte("b"))
	if err == nil || !strings.Contains(err.Error(), "ran out of suffixes") {
		t.Errorf("expected exhaustion error, got %v", err)
	}
}

// --- PromoteEligibleRules --------------------------------------------------------------

func TestPromoteEligibleRules_GatesAndPartialFailure(t *testing.T) {
	dir := t.TempDir()
	rules := []LearnedRule{
		{Pattern: "no score entry at all"},
		{Pattern: "recall too low"},
		{Pattern: "composite too low"},
		{Pattern: "fully eligible rule"},
		{Pattern: ""}, // eligible score but promotion fails (empty pattern)
	}
	scores := map[string]ScoreResult{
		"recall too low":      {RecallCount: 9, Composite: 0.99},  // < default 10
		"composite too low":   {RecallCount: 50, Composite: 0.84}, // < default 0.85
		"fully eligible rule": {RecallCount: 12, Composite: 0.90},
		"":                    {RecallCount: 12, Composite: 0.90},
	}
	// MinRecall/MinComposite zero → defaults (10 / 0.85) kick in.
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{OutputDir: dir})
	if err == nil || !strings.Contains(err.Error(), "pattern is empty") {
		t.Errorf("firstErr should surface the empty-pattern failure, got %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("written = %v, want exactly the eligible rule", written)
	}
	if !strings.Contains(written[0], "skill-fully-eligible-rule.md") {
		t.Errorf("wrong file written: %s", written[0])
	}
	if _, statErr := os.Stat(written[0]); statErr != nil {
		t.Errorf("written path missing on disk: %v", statErr)
	}
}

func TestPromoteEligibleRules_CustomThresholds(t *testing.T) {
	dir := t.TempDir()
	rules := []LearnedRule{{Pattern: "loose gate rule"}}
	scores := map[string]ScoreResult{"loose gate rule": {RecallCount: 2, Composite: 0.5}}

	// Defaults would reject this rule; explicit looser gates accept it.
	written, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir: dir, MinRecall: 1, MinComposite: 0.4,
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(written) != 1 {
		t.Errorf("custom thresholds should admit the rule, got %v", written)
	}
}

// --- buildDescription / safeYAMLString ---------------------------------------------------

func TestBuildDescription(t *testing.T) {
	t.Run("short input padded past lint floor", func(t *testing.T) {
		d := buildDescription("x", "")
		if len(d) < 30 {
			t.Errorf("len = %d, want >= 30: %q", len(d), d)
		}
		if !strings.HasPrefix(d, "Use when x") {
			t.Errorf("missing trigger prefix: %q", d)
		}
	})
	t.Run("action joined with em dash", func(t *testing.T) {
		d := buildDescription("builds fail", "pin the toolchain")
		if d != "Use when builds fail — pin the toolchain" {
			t.Errorf("got %q", d)
		}
	})
	t.Run("whitespace normalised", func(t *testing.T) {
		d := buildDescription("a\nmultiline\tpattern", "an\r\naction")
		if strings.ContainsAny(d, "\n\r\t") {
			t.Errorf("control whitespace survived: %q", d)
		}
		if !strings.Contains(d, "a multiline pattern") {
			t.Errorf("words not space-joined: %q", d)
		}
	})
	t.Run("long input truncated with ellipsis", func(t *testing.T) {
		d := buildDescription(strings.Repeat("a", 500), "")
		if !strings.HasSuffix(d, "…") {
			t.Errorf("missing ellipsis: %q", d[len(d)-10:])
		}
		if len(d) > 410 { // 399 bytes + 3-byte ellipsis
			t.Errorf("not truncated: len=%d", len(d))
		}
	})
}

func TestSafeYAMLString(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`plain`, `"plain"`},
		{`has "quotes"`, `"has \"quotes\""`},
		{`back\slash`, `"back\\slash"`},
		{"new\nline", `"new\nline"`},
		{"car\rriage", `"car\rriage"`},
		{"ta\tb", `"ta\tb"`},
		{`colon: hash # dash -`, `"colon: hash # dash -"`},
	}
	for _, tc := range cases {
		if got := safeYAMLString(tc.in); got != tc.want {
			t.Errorf("safeYAMLString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}
