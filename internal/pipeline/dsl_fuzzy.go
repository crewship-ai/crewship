package pipeline

import (
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/fuzzy"
)

// didYouMean formats a short "(did you mean: a, b, c?)" suffix for an
// unresolved slug/name reference, or "" when nothing is close enough to
// suggest. Backs the agent_slug / grader_agent_slug / input-name typo hints
// in Validate (#1423 item 1) — the same ranking cmd/crewship's `routine
// doctor` uses for routine-slug not-found hints (slug_suggest.go), factored
// into internal/fuzzy so both packages share one implementation instead of
// growing a second one here.
func didYouMean(target string, pool []string) string {
	hits := fuzzy.Nearest(target, pool, 3)
	if len(hits) == 0 {
		return ""
	}
	return " (did you mean: " + strings.Join(hits, ", ") + "?)"
}

// sortedSetKeys returns a string-set map's keys in deterministic (sorted)
// order. fuzzy.Nearest's tie-breaking is only meaningful over a
// deterministic input pool, and Go map iteration order isn't. Named
// distinctly from code_runtimes.go's sortedKeys(map[string]bool) — same
// idea, different map value type, not worth a generic for two call shapes.
func sortedSetKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
