package api

import (
	"bytes"
	"encoding/json"
	"github.com/pmezard/go-difflib/difflib"
)

// prettyJSONOrRaw re-indents a definition_json blob for a readable diff;
// falls back to the raw string (still correct, just less pretty) if it
// somehow isn't valid JSON — a stored definition should always parse, but
// the diff endpoint is read-only tooling and must never 500 on a
// malformed historical row.
func prettyJSONOrRaw(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return raw
	}
	return buf.String()
}

// buildUnifiedDiff renders a 3-line-context unified diff between
// `before` and `after` and counts +/- lines. Counts deliberately
// exclude the diff header lines ("---", "+++", "@@") — the UI
// renders those as chrome, not content, so the numeric badge
// should reflect actual content lines added or removed.
//
// The append-only nature of the merge (current bytes are a
// strict prefix of merged bytes) means deletions are always 0
// today; the function still computes both so a future change
// to the append shape (e.g. dedup against a prior rule block)
// would surface in the stats without re-plumbing.
func buildUnifiedDiff(before, after []byte) (string, int, int, error) {
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(before)),
		B:        difflib.SplitLines(string(after)),
		FromFile: "canonical (current)",
		ToFile:   "canonical (post-merge)",
		Context:  3,
	}
	out, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return "", 0, 0, err
	}
	additions, deletions := countDiffEdits(out)
	return out, additions, deletions, nil
}
