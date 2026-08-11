package automation

import (
	"sort"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Rejection is the clause that kept the most entries out, and why.
type Rejection struct {
	Clause    string `json:"clause,omitempty"`
	Count     int    `json:"count,omitempty"`
	Detail    string `json:"detail,omitempty"`
	KeyAbsent bool   `json:"key_absent,omitempty"`
}

// PreviewResult is what a rule WOULD have done to entries already written.
type PreviewResult struct {
	// Scanned counts entries of the rule's event type. Entries of any other
	// type are not this rule's business and are not evidence about it.
	Scanned int `json:"scanned"`
	Matched int `json:"matched"`
	// Samples are the first few matches, so "3 matched" can be checked
	// rather than believed.
	Samples []journal.Entry `json:"samples,omitempty"`
	// TopRejection is populated only when nothing matched. With several
	// failing clauses it names the one that excluded the most: fixing a
	// predicate that rejected one entry while another rejects all of them
	// is a wasted round trip.
	TopRejection Rejection `json:"top_rejection,omitempty"`
}

const previewSamples = 5

// Preview replays already-written entries against a matcher.
//
// This is the answer to the failure mode `automation create` warns about in
// its own help text: a rule that never fires, with nothing to say so. The
// documented first example shipped predicating on a payload key the event
// does not carry, so the very first rule a reader built did nothing and told
// them nothing. Judging a rule against history turns that into an answer
// available BEFORE the rule is trusted.
//
// Pure: no clock, no database, no I/O. The caller decides which slice of
// history to judge.
func Preview(m Matcher, eventType string, entries []journal.Entry) PreviewResult {
	var out PreviewResult
	// Per clause: how many entries it rejected, and the first explanation,
	// which is representative because a clause rejects for one reason.
	counts := map[string]int{}
	first := map[string]MatchResult{}

	for _, e := range entries {
		if eventType != "" && string(e.Type) != eventType {
			continue
		}
		out.Scanned++
		r := m.Explain(e)
		if r.Matched {
			out.Matched++
			if len(out.Samples) < previewSamples {
				out.Samples = append(out.Samples, e)
			}
			continue
		}
		counts[r.Clause]++
		if _, seen := first[r.Clause]; !seen {
			first[r.Clause] = r
		}
	}

	// A rule that matched something is working; naming a clause that also
	// rejected some entries would read as a fault.
	if out.Matched > 0 || out.Scanned == 0 {
		return out
	}

	clauses := make([]string, 0, len(counts))
	for c := range counts {
		clauses = append(clauses, c)
	}
	// Count desc, then name — so a tie reports the same clause every run
	// rather than looking like a flake.
	sort.Slice(clauses, func(i, j int) bool {
		if counts[clauses[i]] != counts[clauses[j]] {
			return counts[clauses[i]] > counts[clauses[j]]
		}
		return clauses[i] < clauses[j]
	})
	if len(clauses) > 0 {
		top := clauses[0]
		out.TopRejection = Rejection{
			Clause: top, Count: counts[top],
			Detail: first[top].Detail, KeyAbsent: first[top].KeyAbsent,
		}
	}
	return out
}
