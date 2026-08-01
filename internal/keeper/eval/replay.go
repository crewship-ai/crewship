package eval

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
)

// Production replay settings — these MUST mirror the live gatekeeper call
// (internal/keeper/gatekeeper Evaluate) so replayed decisions are comparable to
// the recorded ones. Same temperature, same token cap, same per-call timeout.
// replayVerdictSchema is the credential path's decision space, mirroring
// gatekeeper.verdictSchema for RequestTypeAccess. Three verbs, because the
// corpus this harness replays is the credential corpus; the behavior watchdog's
// four-verb space is not scored here (see corpusRequestTypes).
var replayVerdictSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"decision": map[string]any{"type": "string", "enum": []string{"ALLOW", "DENY", "ESCALATE"}},
		"reason":   map[string]any{"type": "string"},
		"risk":     map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
	},
	"required": []string{"decision", "reason", "risk"},
}

const (
	replayTemperature = 0.1
	replayMaxTokens   = 256
	replayTimeout     = 5 * time.Second
)

// DefaultPasses is the replay pass count. Temperature is non-zero (0.1), so a
// single pass is non-deterministic; the scorer takes the worst case across
// passes for the safety metric, so more passes = a stricter dangerous-flip
// estimate. 3 matches the spec's default.
const DefaultPasses = 3

// Candidate names a model to replay through a given provider. The incumbent is
// modelled as just another Candidate (Label "incumbent") so it is scored on the
// exact same corpus and code path — the reference ceiling, not a special case.
type Candidate struct {
	Label    string // display label, e.g. "qwen2.5:3b-instruct" or "incumbent"
	Provider llm.Provider
	Model    string
}

// ReplayCandidate runs each corpus prompt through the candidate `passes` times
// at the production settings and returns scorer Rows (the reference label plus N
// replayed outcomes). passes < 1 is treated as 1.
//
// The label and its source are carried straight through from the CorpusRow. The
// driver never decides what a row is worth — that judgement belongs to
// LoadCorpus, which is the only place that has seen the resolution columns.
//
// A provider/transport error or an unparseable response is scored as a
// fail-closed DENY at risk 10 — exactly what the live gatekeeper records when
// the model is unavailable or returns garbage. That keeps the harness honest:
// a model that errors or emits junk is never silently credited with agreement,
// and a labelled DENY it "reproduces" by failing is not counted as a dangerous
// flip (DENY→DENY), while a labelled ALLOW it fails to reproduce shows up as a
// disagreement rather than being hidden.
//
// The context governs the whole run; each individual model call additionally
// gets its own replayTimeout. A cancelled context aborts between rows.
func ReplayCandidate(ctx context.Context, c Candidate, corpus []CorpusRow, passes int) ([]Row, error) {
	if passes < 1 {
		passes = 1
	}
	temp := replayTemperature
	rows := make([]Row, 0, len(corpus))
	for _, cr := range corpus {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := Row{
			Label:         cr.Label,
			Source:        cr.LabelSource,
			IncumbentRisk: cr.IncumbentRisk,
			Replays:       make([]Replay, 0, passes),
		}
		for p := 0; p < passes; p++ {
			row.Replays = append(row.Replays, replayOnce(ctx, c, cr.Prompt, &temp))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// replayOnce sends one prompt to the candidate at the production settings and
// normalizes the response with the gatekeeper's shared fail-closed rules
// (NormalizeRawResponse) so a replayed decision is scored identically to how
// production would have recorded it.
func replayOnce(ctx context.Context, c Candidate, prompt string, temp *float64) Replay {
	callCtx, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()

	resp, err := c.Provider.Complete(callCtx, llm.Request{
		Model:       c.Model,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		Temperature: temp,
		MaxTokens:   replayMaxTokens,
		// Matches the live judge: constrained decoding on, reasoning off.
		//
		// Format was added to the gatekeeper and to all three judge probes with
		// the stated rationale that a probe asking an easier or harder question
		// than production measures the wrong thing — and this driver, whose own
		// comment says it MUST mirror the live call, was left out. Without it the
		// harness asks a candidate to volunteer well-formed JSON from prose, so a
		// model that answers perfectly in production scores as unusable: its reply
		// opens with prose, NormalizeRawResponse fails, and replayOnce records the
		// fail-closed DENY risk 10.
		Format: replayVerdictSchema,
		// Reasoning off, for the same reason: replaying a thinking model with it
		// on burns the same budget on a chain of thought and scores it as a
		// blanket DENY — the eval would then rank it for its truncation, not its
		// judgement.
		Think: func() *bool { b := false; return &b }(),
	})
	if err != nil {
		// Unavailable model → deny-by-default, mirroring the live gatekeeper.
		return Replay{Decision: Deny, Risk: 10}
	}

	decision, risk, _, _ := gatekeeper.NormalizeRawResponse(resp.Content)
	return Replay{Decision: Decision(decision), Risk: risk}
}
