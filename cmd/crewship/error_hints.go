package main

import "github.com/crewship-ai/crewship/internal/fuzzy"

// Thin CLI-local aliases for "did you mean?"-style error message helpers.
// The canonical Levenshtein/ranking implementation moved to internal/fuzzy
// as part of #1423 item 1, so internal/pipeline's offline `routine validate`
// (agent_slug / input-name did-you-mean) can share it — package main can't
// be imported from internal/pipeline, so the algorithm had to live in an
// importable internal package, not here. These wrappers exist only so the
// many existing cmd/crewship call sites (slug_suggest.go, cmd_helpers.go,
// cmd_prompt.go, routine_doctor_agents.go) didn't all need touching.

func levenshtein(a, b string) int { return fuzzy.Levenshtein(a, b) }

func nearestSlugs(target string, pool []string, maxN int) []string {
	return fuzzy.Nearest(target, pool, maxN)
}

func truncateList(in []string, maxN int) []string { return fuzzy.TruncateList(in, maxN) }

func itoa(n int) string { return fuzzy.Itoa(n) }
