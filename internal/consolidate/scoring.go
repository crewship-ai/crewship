package consolidate

import (
	"math"
	"time"
)

// Six-signal scoring for promotion of candidate rules from session
// memory into the canonical store. The weights:
//
//	Relevance           0.30
//	Frequency           0.24
//	Query diversity     0.15
//	Recency             0.15
//	Consolidation       0.10
//	Conceptual richness 0.06
//	-------------------------
//	Sum                 1.00
//
// Promotion gates:
//
//	score          >= 0.80
//	recall_count   >= 3   (rule referenced/recalled in ≥3 distinct retrievals)
//	unique_queries >= 3   (matched ≥3 distinct query strings)
//
// All three must pass — the recall + unique-query gates filter out
// high-score one-off candidates that the LLM merely felt confident
// about. Weights and thresholds are configurable; the defaults below
// are a documented starting baseline, revisitable once production
// telemetry surfaces what to tune.

// SignalWeights captures the six weights as a struct so callers can
// override for experimentation (paired with the runner option). Use
// DefaultSignalWeights for the documented baseline; bespoke weights
// MUST sum to ~1.0 (NormalisedWeights enforces this on construction).
type SignalWeights struct {
	Relevance          float64
	Frequency          float64
	QueryDiversity     float64
	Recency            float64
	Consolidation      float64
	ConceptualRichness float64
}

// DefaultSignalWeights returns the documented baseline weights. Sum
// is exactly 1.00.
func DefaultSignalWeights() SignalWeights {
	return SignalWeights{
		Relevance:          0.30,
		Frequency:          0.24,
		QueryDiversity:     0.15,
		Recency:            0.15,
		Consolidation:      0.10,
		ConceptualRichness: 0.06,
	}
}

// Sum returns the total weight. Used by callers experimenting with
// custom weights to validate the sum is in tolerance.
func (w SignalWeights) Sum() float64 {
	return w.Relevance + w.Frequency + w.QueryDiversity +
		w.Recency + w.Consolidation + w.ConceptualRichness
}

// PromotionThresholds carries the three gating conditions a candidate
// must pass to land in canonical memory. The published defaults
// (MinScore 0.80, MinRecallCount 3, MinUniqueQueries 3) are designed
// to keep the canonical corpus tight — a rule must show up in at
// least 3 separate retrievals across 3 distinct queries before it's
// promoted. Below threshold candidates get logged but not promoted.
type PromotionThresholds struct {
	MinScore         float64
	MinRecallCount   int
	MinUniqueQueries int
}

// DefaultThresholds returns the documented baseline gating constants.
func DefaultThresholds() PromotionThresholds {
	return PromotionThresholds{
		MinScore:         0.80,
		MinRecallCount:   3,
		MinUniqueQueries: 3,
	}
}

// SignalScores carries the per-signal raw scores in the [0,1] range.
// Each signal is documented below in the file with its scoring
// function. The final composite score is dot-product against the
// weights — see ComputeScore.
type SignalScores struct {
	Relevance          float64
	Frequency          float64
	QueryDiversity     float64
	Recency            float64
	Consolidation      float64
	ConceptualRichness float64
}

// ScoreResult is what ComputeScore returns: the per-signal breakdown
// (auditor-friendly), the weighted composite (the decision number),
// and a "promoted" boolean computed against the supplied thresholds.
// JSON tags align with the explain endpoint's eventual response shape.
type ScoreResult struct {
	Signals       SignalScores        `json:"signals"`
	Weights       SignalWeights       `json:"weights"`
	Composite     float64             `json:"composite"`
	RecallCount   int                 `json:"recall_count"`
	UniqueQueries int                 `json:"unique_queries"`
	Promoted      bool                `json:"promoted"`
	Thresholds    PromotionThresholds `json:"thresholds"`
}

// CandidateMetrics is the input to ComputeScore. Callers (the
// consolidator, when materialising rules from the journal) populate
// these from journal queries + the candidate's own metadata; the
// computer is intentionally pure-data so it can be unit-tested
// without any I/O.
//
// Field semantics:
//
//   - RawRelevance: a 0..1 score derived from how strongly the
//     candidate matched the queries that retrieved it. The
//     consolidator currently fills this from the LLM's confidence
//     (LearnedRule.Confidence); future iterations can replace with
//     BM25/cosine averages.
//   - RecallCount: number of distinct recall events that surfaced
//     this candidate's evidence in the look-back window.
//   - UniqueQueries: distinct query strings (case-folded, normalised)
//     that hit the candidate's evidence.
//   - ConsolidationCount: how many separate consolidator runs have
//     already touched this pattern (1 = first time seen).
//   - LastSeenAt: timestamp of the most recent recall. Used for
//     Recency decay.
//   - EvidenceCount: number of journal entries cited as evidence.
//     Drives Conceptual richness (richer evidence = more entries,
//     more diverse entry types).
//   - DistinctEntryTypes: count of unique entry_type values among
//     the evidence. Drives the second half of Conceptual richness.
//
// Computers below transform these into the [0,1] signal values.
type CandidateMetrics struct {
	RawRelevance       float64
	RecallCount        int
	UniqueQueries      int
	ConsolidationCount int
	LastSeenAt         time.Time
	EvidenceCount      int
	DistinctEntryTypes int
}

// ComputeScore returns the per-signal breakdown + the weighted
// composite + the promotion decision. All math is documented in the
// helper functions; the composite is plain dot-product.
//
// Decision rule:
//
//	promoted == score >= thresh.MinScore
//	         && recallCount >= thresh.MinRecallCount
//	         && uniqueQueries >= thresh.MinUniqueQueries
//
// All three must pass — score alone is necessary but not sufficient.
// The recall + unique-query gates exist to filter out high-score
// one-off candidates that the LLM merely felt confident about.
func ComputeScore(m CandidateMetrics, w SignalWeights, thresh PromotionThresholds, now time.Time) ScoreResult {
	s := SignalScores{
		Relevance:          relevanceScore(m.RawRelevance),
		Frequency:          frequencyScore(m.RecallCount),
		QueryDiversity:     queryDiversityScore(m.UniqueQueries),
		Recency:            recencyScore(m.LastSeenAt, now),
		Consolidation:      consolidationScore(m.ConsolidationCount),
		ConceptualRichness: conceptualRichnessScore(m.EvidenceCount, m.DistinctEntryTypes),
	}
	composite := s.Relevance*w.Relevance +
		s.Frequency*w.Frequency +
		s.QueryDiversity*w.QueryDiversity +
		s.Recency*w.Recency +
		s.Consolidation*w.Consolidation +
		s.ConceptualRichness*w.ConceptualRichness

	promoted := composite >= thresh.MinScore &&
		m.RecallCount >= thresh.MinRecallCount &&
		m.UniqueQueries >= thresh.MinUniqueQueries

	return ScoreResult{
		Signals:       s,
		Weights:       w,
		Composite:     composite,
		RecallCount:   m.RecallCount,
		UniqueQueries: m.UniqueQueries,
		Promoted:      promoted,
		Thresholds:    thresh,
	}
}

// relevanceScore: clamp raw [0,1] confidence to [0,1]. The LLM-side
// confidence already lives in that range per LearnedRule's
// definition; the clamp guards against an out-of-spec response.
func relevanceScore(raw float64) float64 {
	if math.IsNaN(raw) || raw < 0 {
		return 0
	}
	if raw > 1 {
		return 1
	}
	return raw
}

// frequencyScore: log-shaped curve so the first few recalls move the
// needle most. Saturates near 1.0 at ~30 recalls. Picked because
// real-world recall counts are long-tailed — a flat linear scale
// would let a single popular rule dominate the score regardless of
// quality.
//
// Formula: log(1+count) / log(1+30). Sat at 30 because that's
// roughly one recall every other day over a 60-day window — the
// canonical "this matters enough to keep forever" threshold.
func frequencyScore(count int) float64 {
	if count <= 0 {
		return 0
	}
	const saturation = 30
	score := math.Log(1+float64(count)) / math.Log(1+saturation)
	if score > 1 {
		return 1
	}
	return score
}

// queryDiversityScore: another log curve, saturating sooner because
// unique queries plateau faster than recall count — 10 distinct
// queries hitting the same rule is already exceptional coverage.
func queryDiversityScore(unique int) float64 {
	if unique <= 0 {
		return 0
	}
	const saturation = 10
	score := math.Log(1+float64(unique)) / math.Log(1+saturation)
	if score > 1 {
		return 1
	}
	return score
}

// recencyScore: exponential decay with a 14-day half-life. A rule
// recalled today scores 1.0; same rule untouched for two weeks
// scores 0.5; four weeks 0.25; etc. Half-life picked to align with
// the typical "did this still matter last sprint" cadence for
// coding-agent work.
func recencyScore(lastSeen, now time.Time) float64 {
	if lastSeen.IsZero() {
		return 0
	}
	age := now.Sub(lastSeen)
	if age < 0 {
		// Clock skew: treat future timestamps as "very recent".
		return 1
	}
	const halfLife = 14 * 24 * time.Hour
	// exp(-ln2 * age / halfLife)
	return math.Exp(-math.Ln2 * float64(age) / float64(halfLife))
}

// consolidationScore: rises with how many consolidator runs have
// touched this pattern. A pattern seen across many runs is more
// stable than one that appeared once. Linear ramp to 5 runs then
// saturates — past 5, additional runs add little new information.
func consolidationScore(count int) float64 {
	if count <= 0 {
		return 0
	}
	const saturation = 5
	if count >= saturation {
		return 1
	}
	return float64(count) / float64(saturation)
}

// conceptualRichnessScore combines two sub-signals:
//
//   - Evidence breadth: more entries cited = richer context for the
//     rule. Saturates at 8 entries (above which more entries add
//     noise more than signal in practice).
//   - Type diversity: more distinct entry types = the rule
//     generalises across different surfaces (peer escalation +
//     mission status + keeper decision = real cross-cutting pattern,
//     vs. five peer escalations = single-surface noise). Saturates
//     at 4 distinct types.
//
// Mixed 70/30 in favour of type diversity because diversity is a
// stronger anti-overfit signal than raw count.
func conceptualRichnessScore(evidenceCount, distinctTypes int) float64 {
	breadth := math.Min(float64(evidenceCount)/8.0, 1.0)
	diversity := math.Min(float64(distinctTypes)/4.0, 1.0)
	return 0.3*breadth + 0.7*diversity
}
