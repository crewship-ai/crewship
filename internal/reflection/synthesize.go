package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/crewship-ai/crewship/internal/quartermaster"
)

// synthesisRubric is the rubric the judge grades against when producing
// the keep/revise/reject plan. Order is stabilized here; the
// quartermaster ensemble layer will shuffle it per judge to cancel
// position bias.
var synthesisRubric = []string{
	"Do the three critiques agree on the main failure modes, or do they contradict each other?",
	"Are the suggested revisions concrete and actionable, or are they vague?",
	"Is anything in the subject worth preserving as-is?",
	"Are any critique points out of scope for this persona and should be discarded?",
}

// synthesisPrompt is the user-side prompt fed to the judge. It lists all
// critiques with their severity and instructs the judge to emit a JSON
// envelope matching Synthesis.
const synthesisPromptTemplate = `You are synthesizing a multi-reviewer critique panel.

The subject under review is between ===SUBJECT=== markers. Below the
subject are critiques from multiple reviewers, each with a severity rating.

Your job is to produce a single consolidated plan:
- "keep": prose describing what in the subject is already correct and
  should not change.
- "revise": an ordered list of {what, why} pairs — each a targeted edit.
- "reject": critique points that should be discarded (contradicted by
  another reviewer, out of scope, or unsupported).
- "confidence": your certainty in this synthesis, in [0, 1].

Respond ONLY with JSON:
{
  "keep": "...",
  "revise": [{"what": "...", "why": "..."}],
  "reject": ["..."],
  "confidence": 0.0
}

===SUBJECT===
%s
===END SUBJECT===

===CRITIQUES===
%s
===END CRITIQUES===`

// Synthesize asks the supplied JudgeInterface to merge the per-persona
// critiques into a single Synthesis. The judge also returns a verdict
// (score + confidence + reasoning) that the caller can forward to
// regression / eval pipelines unchanged.
//
// Dampening: if the per-judge score spread embedded in the verdict is
// wide (population stddev > 0.25) we reduce the Synthesis.Confidence by
// 25%. Disagreement among judges is a real signal that the synthesis is
// less trustworthy, even if the judge itself reported high confidence in
// the output schema.
//
// The function requires a non-nil judge and at least one critique. An
// empty critique slice is a programming error — the caller should have
// handled that upstream, e.g. by skipping reflection entirely.
func Synthesize(ctx context.Context, judge quartermaster.JudgeInterface, critiques []Critique, subject string) (Synthesis, quartermaster.JudgeVerdict, error) {
	if judge == nil {
		return Synthesis{}, quartermaster.JudgeVerdict{}, fmt.Errorf("reflection: Synthesize requires a judge")
	}
	if len(critiques) == 0 {
		return Synthesis{}, quartermaster.JudgeVerdict{}, fmt.Errorf("reflection: Synthesize requires at least one critique")
	}

	prompt := buildSynthesisPrompt(subject, critiques)

	verdict, err := judge.Judge(ctx, prompt, synthesisRubric)
	if err != nil {
		return Synthesis{}, quartermaster.JudgeVerdict{}, fmt.Errorf("reflection: judge: %w", err)
	}

	synth := parseSynthesis(verdict.Reasoning)

	// If the judge's own Scores array suggests ensemble disagreement,
	// dampen confidence. Population stddev > 0.25 is the same threshold
	// quartermaster.EnsembleJudge uses for its "high judge disagreement"
	// annotation, so the signals stay aligned.
	if len(verdict.Scores) > 1 {
		if stddev := popStdDev(verdict.Scores); stddev > 0.25 {
			synth.Confidence = synth.Confidence * 0.75
		}
	}
	// If the judge didn't report confidence in the JSON envelope, fall
	// back to the verdict's confidence so the caller still gets a real
	// number rather than a zero.
	if synth.Confidence == 0 {
		synth.Confidence = verdict.Confidence
	}
	synth.Confidence = clamp01(synth.Confidence)

	return synth, verdict, nil
}

// buildSynthesisPrompt renders the full user-side prompt for the judge.
// Each critique is rendered as a human-readable block with persona,
// severity, issues, and suggestions so the judge can reason over them
// without re-parsing JSON.
func buildSynthesisPrompt(subject string, critiques []Critique) string {
	var blocks strings.Builder
	for i, c := range critiques {
		fmt.Fprintf(&blocks, "--- Reviewer %d: %s (severity=%s) ---\n", i+1, c.Persona, c.Severity)
		if len(c.Issues) > 0 {
			blocks.WriteString("Issues:\n")
			for _, is := range c.Issues {
				fmt.Fprintf(&blocks, "  - %s\n", is)
			}
		}
		if len(c.Suggestions) > 0 {
			blocks.WriteString("Suggestions:\n")
			for _, s := range c.Suggestions {
				fmt.Fprintf(&blocks, "  - %s\n", s)
			}
		}
		if len(c.Issues) == 0 && len(c.Suggestions) == 0 && c.RawText != "" {
			fmt.Fprintf(&blocks, "RawText: %s\n", c.RawText)
		}
		blocks.WriteString("\n")
	}
	return fmt.Sprintf(synthesisPromptTemplate, subject, blocks.String())
}

// parseSynthesis decodes the judge's JSON envelope. Same tolerance as
// parseCritique: first try a direct Unmarshal, then an embedded block,
// then give up and return an empty Synthesis (callers see a confidence
// of 0 and the judge verdict for raw reasoning). We don't error out
// because an unparseable envelope is still useful information — the
// caller has the JudgeVerdict.Reasoning to fall back on.
func parseSynthesis(raw string) Synthesis {
	trimmed := strings.TrimSpace(raw)
	var decoded struct {
		Keep       string `json:"keep"`
		Revise     []struct {
			What string `json:"what"`
			Why  string `json:"why"`
		} `json:"revise"`
		Reject     []string `json:"reject"`
		Confidence float64  `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		block, ok := extractJSONBlock(trimmed)
		if !ok {
			return Synthesis{}
		}
		if err := json.Unmarshal([]byte(block), &decoded); err != nil {
			return Synthesis{}
		}
	}

	revise := make([]RevisionPoint, 0, len(decoded.Revise))
	for _, r := range decoded.Revise {
		revise = append(revise, RevisionPoint{What: r.What, Why: r.Why})
	}

	return Synthesis{
		Keep:       decoded.Keep,
		Revise:     revise,
		Reject:     decoded.Reject,
		Confidence: decoded.Confidence,
	}
}

// popStdDev is the population standard deviation. We duplicate the
// helper from quartermaster (instead of exporting it from there) so
// this package stays a strict consumer of quartermaster's public
// surface.
func popStdDev(xs []float64) float64 {
	if len(xs) <= 1 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	m := sum / float64(len(xs))
	var v float64
	for _, x := range xs {
		d := x - m
		v += d * d
	}
	return math.Sqrt(v / float64(len(xs)))
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
