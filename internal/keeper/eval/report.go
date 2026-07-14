package eval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
)

// LabeledVerdict pairs a candidate's display label with its scored Verdict. It
// is the input to BuildReport, decoupling report assembly from the replay/DB
// layers so the report is fully unit-testable with no model.
type LabeledVerdict struct {
	Label   string
	Verdict Verdict
}

// RankedCandidate is one row of the harness output: a candidate's metrics plus
// its deltas against the incumbent and the incumbent-relative viability gate.
type RankedCandidate struct {
	Label             string  `json:"label"`
	IsIncumbent       bool    `json:"is_incumbent"`
	Rows              int     `json:"rows"`
	Passes            int     `json:"passes"`
	AgreementRate     float64 `json:"agreement_rate"`
	DangerousFlipRate float64 `json:"dangerous_flip_rate"`
	DangerousFlipRows int     `json:"dangerous_flip_rows"`
	RiskMAE           float64 `json:"risk_mae"`

	// Deltas vs the incumbent (0 for the incumbent row itself).
	AgreementDelta     float64 `json:"agreement_delta"`      // higher is better
	DangerousFlipDelta float64 `json:"dangerous_flip_delta"` // lower is better

	// Viable gates on the safety metric only: a candidate must add no dangerous
	// flips beyond the incumbent (within tolerance). Always true for the
	// incumbent itself.
	Viable bool `json:"viable"`
}

// Report is the full harness output: the incumbent baseline plus every
// candidate, ranked safest-first. It marshals directly to the machine-readable
// JSON the spec (§3) calls for.
type Report struct {
	Incumbent  string            `json:"incumbent"`
	Tolerance  float64           `json:"tolerance"`
	Candidates []RankedCandidate `json:"candidates"`
}

// BuildReport scores each candidate against the incumbent and ranks the result.
//
// Ranking is deliberately safety-first: the incumbent is pinned to the top as
// the reference ceiling, then candidates are ordered by dangerous-flip rate
// ascending (the metric that actually matters), agreement rate descending as
// the tiebreaker, then label for stability. A model with higher raw agreement
// but more guard downgrades ranks *below* a safer one — agreement never buys
// its way past a safety regression.
func BuildReport(incumbent LabeledVerdict, candidates []LabeledVerdict, tolerance float64) Report {
	rank := func(lv LabeledVerdict, isIncumbent bool) RankedCandidate {
		cmp := Compare(lv.Verdict, incumbent.Verdict)
		return RankedCandidate{
			Label:              lv.Label,
			IsIncumbent:        isIncumbent,
			Rows:               lv.Verdict.Rows,
			Passes:             lv.Verdict.Passes,
			AgreementRate:      lv.Verdict.AgreementRate,
			DangerousFlipRate:  lv.Verdict.DangerousFlipRate,
			DangerousFlipRows:  lv.Verdict.DangerousFlipRows,
			RiskMAE:            lv.Verdict.RiskMAE,
			AgreementDelta:     cmp.AgreementDelta,
			DangerousFlipDelta: cmp.DangerousFlipDelta,
			Viable:             isIncumbent || cmp.Viable(tolerance),
		}
	}

	ranked := make([]RankedCandidate, 0, len(candidates))
	for _, c := range candidates {
		ranked = append(ranked, rank(c, false))
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].DangerousFlipRate != ranked[j].DangerousFlipRate {
			return ranked[i].DangerousFlipRate < ranked[j].DangerousFlipRate
		}
		if ranked[i].AgreementRate != ranked[j].AgreementRate {
			return ranked[i].AgreementRate > ranked[j].AgreementRate
		}
		return ranked[i].Label < ranked[j].Label
	})

	// Incumbent is always the first row — the baseline every delta is measured
	// against — followed by the safety-ranked candidates.
	out := make([]RankedCandidate, 0, len(candidates)+1)
	out = append(out, rank(incumbent, true))
	out = append(out, ranked...)

	return Report{
		Incumbent:  incumbent.Label,
		Tolerance:  tolerance,
		Candidates: out,
	}
}

// JSON renders the report as indented, machine-readable JSON.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Table renders the report as an aligned, human-readable table. Deltas are
// shown for candidates (blank for the incumbent baseline); the dangerous-flip
// column is first after the label because it is the ranking key.
func (r Report) Table() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Keeper governance-model replay — incumbent=%s, tolerance=%.3f\n",
		r.Incumbent, r.Tolerance)

	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tDANGER_FLIP\tΔFLIP\tAGREE\tΔAGREE\tRISK_MAE\tROWS\tPASSES\tVIABLE")
	for _, c := range r.Candidates {
		label := c.Label
		if c.IsIncumbent {
			label += " (incumbent)"
		}
		flipDelta, agreeDelta := "—", "—"
		if !c.IsIncumbent {
			flipDelta = fmt.Sprintf("%+.3f", c.DangerousFlipDelta)
			agreeDelta = fmt.Sprintf("%+.3f", c.AgreementDelta)
		}
		fmt.Fprintf(tw, "%s\t%.3f (%d)\t%s\t%.3f\t%s\t%.2f\t%d\t%d\t%s\n",
			label,
			c.DangerousFlipRate, c.DangerousFlipRows,
			flipDelta,
			c.AgreementRate,
			agreeDelta,
			c.RiskMAE,
			c.Rows,
			c.Passes,
			viableMark(c.Viable, c.IsIncumbent),
		)
	}
	tw.Flush()
	return sb.String()
}

func viableMark(viable, isIncumbent bool) string {
	switch {
	case isIncumbent:
		return "baseline"
	case viable:
		return "yes"
	default:
		return "NO"
	}
}
