// Package fuzzy provides small, dependency-free "did you mean?" helpers:
// Levenshtein edit distance, nearest-candidate ranking, and a couple of
// string-list display helpers.
//
// Originally lived only in cmd/crewship (error_hints.go) for routine-slug
// and prompt-name suggestions. #1423 item 1 needed the same ranking for
// agent_slug / input-name typos inside internal/pipeline's Validate, which
// cmd/crewship can't be imported from (it's package main) — so the
// canonical implementation moved here, and cmd/crewship/error_hints.go now
// wraps it. One algorithm, two callers.
package fuzzy

// Levenshtein returns the edit distance between a and b. Pure DP,
// O(len(a)*len(b)). Slug/name lengths are typically <30 characters so the
// cost is negligible even across hundreds of candidates.
func Levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Two-row optimisation — we only need the previous row to compute the
	// current one. Saves O(len(a)*len(b)) memory for O(min(len(a), len(b)))
	// — meaningful on long strings, harmless on short.
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// Nearest returns up to maxN candidates from `pool` that are within a
// reasonable edit distance of `target`. Threshold: max(2, len(target)/3) —
// allows a typo or two on short names and proportionally more on longer
// ones.
//
// Sort: by distance ascending, then alphabetical for ties so the order is
// deterministic regardless of slice ordering (callers passing map keys in
// nondeterministic order still get a stable result).
func Nearest(target string, pool []string, maxN int) []string {
	if target == "" || len(pool) == 0 {
		return nil
	}
	threshold := len(target) / 3
	if threshold < 2 {
		threshold = 2
	}

	type scored struct {
		s    string
		dist int
	}
	var matches []scored
	for _, s := range pool {
		d := Levenshtein(target, s)
		if d <= threshold {
			matches = append(matches, scored{s, d})
		}
	}
	// Insertion sort by distance, then alphabetical. The number of matches
	// is small (<= len(pool), realistically < 5) so insertion sort is fine.
	for i := 1; i < len(matches); i++ {
		j := i
		for j > 0 {
			a, b := matches[j-1], matches[j]
			if a.dist > b.dist || (a.dist == b.dist && a.s > b.s) {
				matches[j-1], matches[j] = b, a
				j--
				continue
			}
			break
		}
	}

	if len(matches) > maxN {
		matches = matches[:maxN]
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.s)
	}
	return out
}

// TruncateList returns the first maxN items of in, appending "(+N more)"
// when truncated. Used in error messages so we don't dump hundreds of
// candidates at the user.
func TruncateList(in []string, maxN int) []string {
	if len(in) <= maxN {
		return in
	}
	out := append([]string{}, in[:maxN]...)
	rest := len(in) - maxN
	out = append(out, "(+"+Itoa(rest)+" more)")
	return out
}

// Itoa replaces strconv.Itoa to keep this package dependency-free; callers
// here never need negative numbers.
func Itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
