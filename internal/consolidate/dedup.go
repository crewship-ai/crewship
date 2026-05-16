package consolidate

import (
	"bufio"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// dedupAgainstPrior filters out candidate rules whose normalised
// pattern hash already appears in any `learned-YYYY-MM-DD.md` file in
// outputDir whose date falls within `window` of `now`. The intent is
// "don't re-emit a rule we already wrote in the last week" — a cheap
// guard against the LLM proposing the same pattern across consecutive
// 6h ticks once the underlying journal entries have become stable.
//
// Decision points:
//
//   - Pattern hash, not full structural match. Action / Confidence
//     are allowed to drift between runs (the LLM may sharpen the
//     wording) — pattern is the durable identity of a rule.
//
//   - Normalisation strips leading/trailing whitespace, collapses
//     internal runs of whitespace, and lower-cases. "X Happens" and
//     "  x happens  " hash to the same value. This matches operator
//     intent: a rule about the same event regardless of how the LLM
//     formatted the pattern this time.
//
//   - Files outside the window are NOT scanned. Old learned files
//     become reference material once consolidated into shared memory
//     elsewhere; re-introducing them as dedup blockers would freeze
//     the rule corpus.
//
//   - Failure modes are conservative: an unreadable file or a
//     malformed filename (cannot parse the YYYY-MM-DD) is skipped
//     silently. A dedup pass that crashes is worse than one that
//     misses one prior file.
//
// The function does not mutate its input slice; the returned slice
// is a fresh allocation containing the surviving rules in input order.
func dedupAgainstPrior(rules []LearnedRule, outputDir string, now time.Time, window time.Duration) []LearnedRule {
	if len(rules) == 0 {
		return rules
	}
	if window <= 0 {
		window = 7 * 24 * time.Hour
	}
	priors := loadPriorPatternHashes(outputDir, now, window)
	if len(priors) == 0 {
		return rules
	}
	out := make([]LearnedRule, 0, len(rules))
	for _, r := range rules {
		if _, dup := priors[hashPattern(r.Pattern)]; dup {
			continue
		}
		out = append(out, r)
	}
	return out
}

// hashPattern returns the FNV-64 hash of the normalised pattern
// string. Normalisation: trim, collapse internal whitespace, lower-case.
func hashPattern(pattern string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(normalisePattern(pattern)))
	return h.Sum64()
}

func normalisePattern(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Collapse internal whitespace runs into single spaces.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// loadPriorPatternHashes scans outputDir for learned-YYYY-MM-DD.md
// files whose date is within `window` of `now`, parses out the
// `**Pattern:**` lines, hashes them, and returns the set. Files whose
// name does not parse as a date are skipped silently — the consolidator
// only writes the canonical name, so a mis-named file is presumed to
// be a human artefact (notes, copy/paste, backup) that should not
// participate in dedup.
func loadPriorPatternHashes(outputDir string, now time.Time, window time.Duration) map[uint64]struct{} {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
	}
	cutoff := now.Add(-window)
	out := make(map[uint64]struct{}, 32)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "learned-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "learned-"), ".md")
		t, perr := time.Parse("2006-01-02", dateStr)
		if perr != nil {
			continue
		}
		// The file is dated by day; treat anything on-or-after cutoff
		// as in-window. End of the day is the same as the start for
		// this purpose (we don't have per-rule timestamps inside the
		// file).
		if t.Before(cutoff.Truncate(24 * time.Hour)) {
			continue
		}
		path := filepath.Join(outputDir, name)
		for _, p := range extractPatterns(path) {
			out[hashPattern(p)] = struct{}{}
		}
	}
	return out
}

// extractPatterns reads a learned-*.md and returns the value of every
// "**Pattern:**" line. Robust to the rendered markdown shape the
// consolidator emits: `- **Pattern:** X happens  ` (trailing double-
// space is markdown line break; trailing newline is line terminator).
func extractPatterns(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const marker = "**Pattern:**"
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		val := strings.TrimSpace(line[idx+len(marker):])
		// Trailing markdown line-break markers ("  ").
		val = strings.TrimRight(val, " ")
		if val != "" {
			out = append(out, val)
		}
	}
	return out
}
