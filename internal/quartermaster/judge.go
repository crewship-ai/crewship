package quartermaster

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// JudgeInterface is the provider-neutral contract for an LLM-as-judge.
// Callers plug in an Ollama / Anthropic / stub adapter; this package does
// not import any LLM SDK. The judge is expected to return a score in
// [0, 1], a confidence in [0, 1], prose reasoning, and (optionally) echo
// the rubric it graded against so the caller can verify the ordering the
// judge actually saw.
type JudgeInterface interface {
	Judge(ctx context.Context, prompt string, rubric []string) (JudgeVerdict, error)
}

// EnsembleJudge runs k random judges from the pool in parallel-equivalent
// order (the caller's context cancels all of them if it fires). Each
// judge sees a shuffled copy of the rubric, which mitigates position bias
// (LLM judges tend to overweight the first / last rubric item). The
// final verdict uses the median score across judges and averages the
// confidences.
//
// If the spread of scores is high (population stddev > 0.25) the
// reasoning field is annotated with a "high judge disagreement" warning
// so downstream consumers can treat the verdict as lower-trust.
//
// If the averaged confidence is below 0.9 the verdict's HumanEscalate
// flag is set, signaling a manual review is required before the verdict
// is acted on.
//
// k is clamped to [1, len(judges)]. seed parameterizes the judge selection
// and per-judge rubric shuffle for reproducibility.
func EnsembleJudge(ctx context.Context, judges []JudgeInterface, prompt string, rubric []string, k int, seed uint64) (JudgeVerdict, error) {
	if len(judges) == 0 {
		return JudgeVerdict{}, fmt.Errorf("quartermaster: ensemble requires at least one judge")
	}
	if k <= 0 {
		k = 3
	}
	if k > len(judges) {
		k = len(judges)
	}

	// Seed a deterministic PCG source — caller provides the seed so the
	// entire ensemble is reproducible for a given input.
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))

	// Pick k judges without replacement.
	idx := make([]int, len(judges))
	for i := range idx {
		idx[i] = i
	}
	rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	picked := idx[:k]

	scores := make([]float64, 0, k)
	confidences := make([]float64, 0, k)
	reasonings := make([]string, 0, k)

	for _, pi := range picked {
		// Shuffle rubric per judge to cancel position bias.
		shuffled := make([]string, len(rubric))
		copy(shuffled, rubric)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

		v, err := judges[pi].Judge(ctx, prompt, shuffled)
		if err != nil {
			return JudgeVerdict{}, fmt.Errorf("quartermaster: judge %d failed: %w", pi, err)
		}
		scores = append(scores, clamp01(v.Score))
		confidences = append(confidences, clamp01(v.Confidence))
		if v.Reasoning != "" {
			reasonings = append(reasonings, v.Reasoning)
		}
	}

	median := medianOf(scores)
	avgConf := meanOf(confidences)
	stddev := popStdDev(scores)

	reasoning := strings.Join(reasonings, " | ")
	if stddev > 0.25 {
		if reasoning != "" {
			reasoning = "high judge disagreement (stddev=" + formatFloat(stddev) + "); " + reasoning
		} else {
			reasoning = "high judge disagreement (stddev=" + formatFloat(stddev) + ")"
		}
	}

	verdict := JudgeVerdict{
		Score:      median,
		Confidence: avgConf,
		Reasoning:  reasoning,
		Rubric:     append([]string(nil), rubric...),
		Scores:     scores,
	}
	if avgConf < 0.9 {
		verdict.HumanEscalate = true
	}
	return verdict, nil
}

// clamp01 pins a value to [0, 1]. Judges that return out-of-band values
// are silently corrected rather than errored — we'd rather have a
// clamped score than lose the whole ensemble.
func clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func medianOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// popStdDev is the population standard deviation — we're describing the
// variance of this specific ensemble, not estimating a super-population.
func popStdDev(xs []float64) float64 {
	if len(xs) <= 1 {
		return 0
	}
	m := meanOf(xs)
	sum := 0.0
	for _, x := range xs {
		d := x - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(xs)))
}

func formatFloat(f float64) string {
	// 3 decimal places — enough signal, not enough noise.
	return fmt.Sprintf("%.3f", f)
}
