package eval

import "sort"

// Self-consistency: does the judge agree with ITSELF.
//
// Measured 2026-08-02 on dev2, qwen3.5:9b replayed against its own recorded
// decisions agreed with them 0.625 of the time across three passes. On the same
// prompt, at the same settings, the judge contradicts itself in roughly a third
// of runs.
//
// That reframes every other number here. "Is candidate A better than candidate
// B" presumes each of them is a stable thing to compare; a model that answers
// three ways to one question is not one judge, it is three. It also explains why
// the corpus is hard to label: what is being labelled moves.
//
// # Why this is separate from agreement
//
// Agreement asks whether the judge was RIGHT (against a label). This asks
// whether it was STABLE (against itself), and the two failures need different
// fixes. Consistently wrong is measurable and correctable — better prompt,
// better facts, better model. Unstable is neither, because there is no "it" to
// correct.
//
// # The experiment this exists for
//
// The obvious hypothesis is prompt volume: feed a small model too much and it
// loses the thread. It is the premise behind every retrieval-on-demand memory
// architecture — pull what the decision needs instead of pushing everything you
// have — and adopting one is weeks of work.
//
// So test it before building it. ConsistencyByPromptSize splits the corpus at
// the median prompt length and reports each half. If the long half is markedly
// less stable, prompt volume is a driver and retrieval-on-demand is worth the
// money. If the halves match, it is not, and the subsystem would have been an
// expensive answer to the wrong question.
//
// Either result is worth having, which is what makes it worth running first.

// SelfConsistency is the share of a row's replays that gave the most common
// verdict: 1.0 when every pass agreed, ~0.33 for a three-way split.
//
// Zero for fewer than two replays, and that is deliberate rather than a missing
// case. One sample cannot demonstrate stability, and returning 1.0 would let a
// `--passes 1` run report a perfectly stable judge — the exact reading this
// measurement exists to prevent.
func SelfConsistency(r Row) float64 {
	if len(r.Replays) < 2 {
		return 0
	}
	counts := map[Decision]int{}
	best := 0
	for _, rp := range r.Replays {
		counts[rp.Decision]++
		if counts[rp.Decision] > best {
			best = counts[rp.Decision]
		}
	}
	return float64(best) / float64(len(r.Replays))
}

// ConsistencySegment is one half of the corpus, split by prompt size.
type ConsistencySegment struct {
	// Rows counted — only those with enough replays to show consistency at all.
	Rows int
	// MeanConsistency averages SelfConsistency across those rows.
	MeanConsistency float64
	// MedianPromptChars describes what "short" or "long" meant for this run, so a
	// reader can tell a 4k/6k split from a 400/40000 one. The same ratio means
	// very different things at those two scales.
	MedianPromptChars int
}

// ConsistencyByPromptSize splits rows at the median prompt length and reports
// each half.
//
// Rows that cannot show consistency — fewer than two replays — are excluded
// rather than averaged in as zeros. Counting them would manufacture instability
// out of a single-pass run and make prompt size appear to matter when nothing
// was sampled twice.
//
// Chars rather than tokens on purpose: tokenisation is model-specific and this
// compares one model against itself, where any monotonic proxy for length does
// the same job without pulling in a tokeniser that would then have to be kept in
// step with whatever the judge actually runs.
func ConsistencyByPromptSize(rows []Row) (short, long ConsistencySegment) {
	usable := make([]Row, 0, len(rows))
	for _, r := range rows {
		if len(r.Replays) >= 2 {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		return short, long
	}
	sort.SliceStable(usable, func(i, j int) bool {
		return usable[i].PromptChars < usable[j].PromptChars
	})

	// The shorter half rounds DOWN, so an odd row count puts the median row in
	// the long half. Every row is kept either way — losing the median is how a
	// small corpus quietly becomes a smaller one.
	mid := len(usable) / 2
	segment := func(rs []Row) ConsistencySegment {
		if len(rs) == 0 {
			return ConsistencySegment{}
		}
		var sum float64
		for _, r := range rs {
			sum += SelfConsistency(r)
		}
		return ConsistencySegment{
			Rows:              len(rs),
			MeanConsistency:   sum / float64(len(rs)),
			MedianPromptChars: rs[len(rs)/2].PromptChars,
		}
	}
	return segment(usable[:mid]), segment(usable[mid:])
}
