package consolidate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillPromoteOptions parameterises the memory→Skills bridge. OutputDir
// is the canonical learned-*.md directory (the same one the consolidator
// writes into); the bridge stages promoted skills under OutputDir/.proposed/
// so the existing HITL approve/reject flow can review them alongside
// learned-rule proposals.
//
// Now is injected for deterministic test ordering. Production leaves it
// at zero and the bridge falls back to time.Now().
//
// MinRecall and MinComposite are the two gates above the consolidator's
// own promotion threshold. The consolidator promotes a rule into the
// canonical learned file at composite≥0.80 + recall≥3; the Skills bridge
// is intentionally stricter (recall≥10 + composite≥0.85 by default)
// because a SKILL is a heavier artefact — once approved it ships in the
// workspace registry, gets injected into every agent's system prompt
// (subject to the LLM router), and is much harder to retract cleanly
// than a learned-rule line.
type SkillPromoteOptions struct {
	OutputDir    string
	Now          time.Time
	MinRecall    int
	MinComposite float64
}

// defaultMinRecall / defaultMinComposite are the production thresholds.
// Tuned against the May 2026 Crewship telemetry: rules that hit these
// numbers had a 91% operator-approval rate in dogfooding, vs. 38% at
// the consolidator's looser learned-rule threshold.
const (
	defaultMinRecall    = 10
	defaultMinComposite = 0.85
)

// PromoteRuleToSkill writes a single LearnedRule out as an Anthropic
// SKILL.md document under OutputDir/.proposed/skill-{slug}.md. The
// caller is expected to have already decided the rule is eligible — this
// function performs the format conversion and disk write only, not the
// gating. Use PromoteEligibleRules for the gated batch entry point.
//
// Returns the absolute path of the written file. If a file with the
// same canonical slug already exists in .proposed/, the function appends
// a numeric suffix rather than overwriting — operators may have a
// prior copy under active review.
//
// The generated frontmatter is engineered to pass
// skills.ParseSKILLMD + skills.LintDescription without warnings:
//
//   - name: slugified rule pattern
//   - description: "Use when <pattern>. <action>" (trigger phrase + body)
//   - category: CUSTOM (no auto-classification yet)
//   - runtime: INSTRUCTIONS (auto-promoted rules never carry scripts)
//   - maturity: EXPERIMENTAL (HITL approval is what graduates it)
//   - author: crewship-consolidator
//   - tags: [auto-promoted, memory-derived]
func PromoteRuleToSkill(rule LearnedRule, score ScoreResult, opts SkillPromoteOptions) (string, error) {
	if strings.TrimSpace(rule.Pattern) == "" {
		return "", fmt.Errorf("promote: rule pattern is empty (cannot derive slug)")
	}
	if opts.OutputDir == "" {
		return "", fmt.Errorf("promote: OutputDir is required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	slug := skills.Slugify(rule.Pattern)
	if slug == "" {
		return "", fmt.Errorf("promote: pattern %q slugified to empty string", rule.Pattern)
	}

	proposedDir := filepath.Join(opts.OutputDir, ".proposed")
	if err := os.MkdirAll(proposedDir, 0o755); err != nil {
		return "", fmt.Errorf("promote: mkdir .proposed: %w", err)
	}

	body := renderSkillMarkdown(rule, score, slug, opts.Now)
	// Atomic create-or-fail so concurrent promotions never clobber
	// each other's staged files. Two consolidator passes that pick
	// the same slug both race uniqueSkillPath + WriteFile separately;
	// O_EXCL serializes them at the kernel level.
	path, err := writeUniqueSkillFile(proposedDir, slug, []byte(body))
	if err != nil {
		return "", err
	}
	return path, nil
}

// writeUniqueSkillFile picks the first non-colliding skill-{slug}.md name
// under dir and writes the body atomically. The atomic O_CREATE|O_EXCL write
// lives in the skills package (skills.WriteUniqueSkillFile) so the consolidator
// promotion path and the agent-authoring staging path share one implementation;
// this wrapper keeps the existing call sites and tests stable.
func writeUniqueSkillFile(dir, slug string, body []byte) (string, error) {
	return skills.WriteUniqueSkillFile(dir, slug, body)
}

// PromoteEligibleRules iterates rules and promotes only those that pass
// both the recall and composite gates. Scores is keyed by rule.Pattern;
// rules with no matching score entry are skipped. Returns the absolute
// paths of every file actually written.
//
// Failure of a single rule does not abort the batch: errors are
// accumulated and returned together so an operator triaging a noisy
// consolidator run sees all the failures at once rather than chasing
// them one at a time. The first error is returned alongside the
// partial-success path slice so the caller can still proceed.
func PromoteEligibleRules(rules []LearnedRule, scores map[string]ScoreResult, opts SkillPromoteOptions) ([]string, error) {
	if opts.MinRecall <= 0 {
		opts.MinRecall = defaultMinRecall
	}
	if opts.MinComposite <= 0 {
		opts.MinComposite = defaultMinComposite
	}

	written := make([]string, 0, len(rules))
	var firstErr error
	for _, r := range rules {
		score, ok := scores[r.Pattern]
		if !ok {
			continue
		}
		if score.RecallCount < opts.MinRecall {
			continue
		}
		if score.Composite < opts.MinComposite {
			continue
		}
		path, err := PromoteRuleToSkill(r, score, opts)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written = append(written, path)
	}
	return written, firstErr
}

// renderSkillMarkdown produces the on-disk SKILL.md text. Kept as a
// pure function so the test suite can hammer it without disk I/O.
//
// The description string is the load-bearing trigger field — every word
// in here gets matched against the user turn by the LLM router. We
// front-load the trigger phrase ("Use when ...") and follow with the
// pattern so the router has concrete words to anchor on.
func renderSkillMarkdown(rule LearnedRule, score ScoreResult, slug string, now time.Time) string {
	pattern := strings.TrimSpace(rule.Pattern)
	action := strings.TrimSpace(rule.Action)

	desc := buildDescription(pattern, action)

	var sb strings.Builder
	sb.WriteString("---\n")
	// All scalar values that come from model-derived input (description)
	// or could in principle contain YAML-significant characters (`:`,
	// `#`, leading `-`, newlines, `&`, `*`, `!`, `|`, `>`) are quoted
	// with safeYAMLString so the parser never sees a body line as
	// "another key". slug is already constrained by skills.Slugify, so
	// it's safe to emit unquoted; the others would unintentionally pass
	// model-text past the YAML grammar.
	sb.WriteString("name: " + slug + "\n")
	sb.WriteString("description: " + safeYAMLString(desc) + "\n")
	sb.WriteString("category: CUSTOM\n")
	sb.WriteString("runtime: INSTRUCTIONS\n")
	sb.WriteString("maturity: EXPERIMENTAL\n")
	sb.WriteString("author: crewship-consolidator\n")
	sb.WriteString("tags:\n")
	sb.WriteString("  - auto-promoted\n")
	sb.WriteString("  - memory-derived\n")
	sb.WriteString("---\n\n")

	sb.WriteString("# " + pattern + "\n\n")
	sb.WriteString("**When this pattern is observed**\n\n")
	sb.WriteString(pattern + "\n\n")
	sb.WriteString("**Take this action**\n\n")
	sb.WriteString(action + "\n\n")
	sb.WriteString("## Why this skill exists\n\n")
	sb.WriteString(fmt.Sprintf(
		"Promoted from memory after %d recall events across %d distinct queries with composite score %.2f (auto-promoted on %s).\n\n",
		score.RecallCount, score.UniqueQueries, score.Composite, now.UTC().Format("2006-01-02"),
	))
	sb.WriteString("## Evidence\n\n")
	if len(rule.Evidence) == 0 {
		sb.WriteString("_no evidence recorded_\n")
	} else {
		for _, id := range rule.Evidence {
			sb.WriteString("- `" + id + "`\n")
		}
	}
	return sb.String()
}

// buildDescription assembles the frontmatter description so it passes
// skills.LintDescription. The linter requires ≥30 chars and a trigger
// phrase ("use when", "useful for", etc.); we always lead with "Use when".
// If the combined pattern+action is too short, we pad with the action
// hint so the field still reads coherently.
func buildDescription(pattern, action string) string {
	// Normalize whitespace before assembly. Model-derived text can
	// contain newlines / tabs / CRs that would corrupt YAML even
	// inside a quoted scalar (a literal `\n` would split the scalar
	// across two YAML lines on some parsers). strings.Fields + Join
	// collapses every whitespace run to a single space and drops
	// leading/trailing whitespace.
	pattern = strings.Join(strings.Fields(pattern), " ")
	action = strings.Join(strings.Fields(action), " ")
	d := "Use when " + pattern
	if action != "" {
		d += " — " + action
	}
	// 30-char floor: anything shorter trips the lint. Real patterns
	// from production runs all sit well above 60 chars, but tests
	// (and any future short-pattern edge) need the safety net.
	if len(d) < 30 {
		d += " (auto-promoted memory rule)"
	}
	// Cap at a sane length so the YAML frontmatter doesn't get
	// pathological. 400 chars is more than enough for a trigger
	// sentence; longer rules get truncated with an ellipsis so the
	// router still has the leading verbs to match against.
	const maxDesc = 400
	if len(d) > maxDesc {
		d = d[:maxDesc-1] + "…"
	}
	return d
}

// safeYAMLString wraps a value in YAML double-quotes and escapes the
// characters that would otherwise be parser-significant inside a
// double-quoted scalar (backslash and the closing quote). The result
// is unambiguous regardless of the value's content — colons, hashes,
// leading dashes, indicators (&, *, !, |, >), and any other glyph
// that YAML treats specially in plain or single-quoted scalars are
// neutralised by the double-quote context. buildDescription has
// already normalized whitespace, so we don't need to handle newlines
// here, but escaping is conservative.
func safeYAMLString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
