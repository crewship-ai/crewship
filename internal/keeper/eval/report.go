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

// RankedCandidate is one row of the harness output: a candidate's metrics on the
// human-labelled segment, its deltas against the incumbent, and — separately —
// how much it agrees with the incumbent's own past decisions.
//
// The two agreement numbers are named for what they measure. AgreementRate is
// against people; IncumbentLabelAgreement is against the previous model and is
// not evidence of anything except continuity.
type RankedCandidate struct {
	Label       string `json:"label"`
	IsIncumbent bool   `json:"is_incumbent"`
	Rows        int    `json:"rows"`
	HumanRows   int    `json:"human_rows"`
	Passes      int    `json:"passes"`

	AgreementRate     float64 `json:"agreement_rate"`
	AgreementLow      float64 `json:"agreement_low"`
	AgreementHigh     float64 `json:"agreement_high"`
	DangerousFlipRate float64 `json:"dangerous_flip_rate"`
	DangerousFlipRows int     `json:"dangerous_flip_rows"`
	// The same measure over the incumbent-labelled segment. Reported because the
	// safety gate reads it: a candidate can be spotless on a handful of
	// human-labelled rows and still downgrade guards across the recorded corpus,
	// and a verdict of "no" whose number is nowhere in the output is a verdict an
	// operator cannot check.
	RecordedFlipRate float64 `json:"recorded_flip_rate"`
	RecordedFlipRows int     `json:"recorded_flip_rows"`
	RiskMAE          float64 `json:"risk_mae"`

	// IncumbentLabelRows / IncumbentLabelAgreement describe the rows no human
	// ever ruled on. Reported so a run is transparent about how much of the
	// corpus it could not score for correctness — not so the number can be
	// quoted as accuracy.
	IncumbentLabelRows      int     `json:"incumbent_label_rows"`
	IncumbentLabelAgreement float64 `json:"incumbent_label_agreement"`

	// Deltas vs the incumbent on the human segment (0 for the incumbent row).
	AgreementDelta     float64 `json:"agreement_delta"`      // higher is better
	DangerousFlipDelta float64 `json:"dangerous_flip_delta"` // lower is better

	// Viable gates on ground truth AND safety: the corpus must be big enough to
	// conclude from, and the candidate must add no dangerous flips beyond the
	// incumbent (within tolerance). Blocker says which gate stopped it.
	Viable  bool   `json:"viable"`
	Blocker string `json:"blocker,omitempty"`
}

// Report is the full harness output: the incumbent baseline plus every
// candidate, ranked safest-first. It marshals directly to the machine-readable
// JSON the spec (§3) calls for.
type Report struct {
	Incumbent string  `json:"incumbent"`
	Tolerance float64 `json:"tolerance"`

	// HumanRows / IncumbentLabelRows / Strength describe the CORPUS, not any
	// candidate, and are the first thing both renderers print. A reader who sees
	// only the percentages cannot tell a benchmark from an anecdote; these three
	// fields are what makes the difference visible without having to be looked up.
	HumanRows          int            `json:"human_rows"`
	IncumbentLabelRows int            `json:"incumbent_label_rows"`
	Strength           CorpusStrength `json:"strength"`
	Caveat             string         `json:"caveat"`

	Candidates []RankedCandidate `json:"candidates"`
}

// BuildReport scores each candidate against the incumbent and ranks the result.
//
// Ranking is deliberately safety-first: the incumbent is pinned to the top as
// the reference baseline, then candidates are ordered by dangerous-flip rate
// ascending (the metric that actually matters), agreement rate descending as
// the tiebreaker, then label for stability. A model with higher raw agreement
// but more guard downgrades ranks *below* a safer one — agreement never buys
// its way past a safety regression.
//
// Rank order is computed even when the corpus is too small to support it; the
// per-row Blocker and the report-level Caveat are what stop that order being
// read as a recommendation. Hiding the ranking instead would just move the
// operator to eyeballing raw numbers with no caveat attached at all.
func BuildReport(incumbent LabeledVerdict, candidates []LabeledVerdict, tolerance float64) Report {
	rank := func(lv LabeledVerdict, isIncumbent bool) RankedCandidate {
		cmp := Compare(lv.Verdict, incumbent.Verdict)
		rc := RankedCandidate{
			Label:                   lv.Label,
			IsIncumbent:             isIncumbent,
			Rows:                    lv.Verdict.Rows,
			HumanRows:               lv.Verdict.Human.Rows,
			Passes:                  lv.Verdict.Passes,
			AgreementRate:           lv.Verdict.Human.AgreementRate,
			AgreementLow:            lv.Verdict.Human.AgreementLow,
			AgreementHigh:           lv.Verdict.Human.AgreementHigh,
			DangerousFlipRate:       lv.Verdict.Human.DangerousFlipRate,
			DangerousFlipRows:       lv.Verdict.Human.DangerousFlipRows,
			RecordedFlipRate:        lv.Verdict.ModelLabelled.DangerousFlipRate,
			RecordedFlipRows:        lv.Verdict.ModelLabelled.DangerousFlipRows,
			RiskMAE:                 lv.Verdict.Human.RiskMAE,
			IncumbentLabelRows:      lv.Verdict.ModelLabelled.Rows,
			IncumbentLabelAgreement: lv.Verdict.ModelLabelled.AgreementRate,
			AgreementDelta:          cmp.AgreementDelta,
			DangerousFlipDelta:      cmp.DangerousFlipDelta,
		}
		// The incumbent is compared with itself, so the safety half of the gate
		// can never fire for it — the corpus gate is the only one that can, and
		// it must: reporting the baseline as evidence-backed on six rows is the
		// same false claim as reporting a candidate that way.
		rc.Blocker = cmp.Blocker(tolerance)
		rc.Viable = rc.Blocker == ""
		return rc
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
		Incumbent:          incumbent.Label,
		Tolerance:          tolerance,
		HumanRows:          incumbent.Verdict.Human.Rows,
		IncumbentLabelRows: incumbent.Verdict.ModelLabelled.Rows,
		Strength:           incumbent.Verdict.Strength,
		Caveat:             Caveat(incumbent.Verdict.Human.Rows),
		Candidates:         out,
	}
}

// Caveat is the sentence that goes next to every number this harness prints. It
// is generated from the row count rather than written once in a doc page because
// a caveat that lives somewhere else is a caveat nobody reads.
func Caveat(humanRows int) string {
	switch Strength(humanRows) {
	case StrengthNone:
		return "NO human-labelled rows: nothing here measures correctness. " +
			"Every percentage below is agreement with whatever the previous model decided, " +
			"which is exactly the reading this harness exists to stop. " +
			"Resolve some keeper escalations and re-run."
	case StrengthAnecdotal:
		return fmt.Sprintf("ONLY %d human-labelled row(s) — an anecdote, not a benchmark. "+
			"Rates are withheld because at this size the 95%% interval spans most of the range. "+
			"Need %d rows before a percentage means anything, %d to act on one.",
			humanRows, MinHumanRowsForRate, MinHumanRowsForBenchmark)
	case StrengthIndicative:
		return fmt.Sprintf("%d human-labelled rows — indicative. Wide intervals: good enough to "+
			"see a large effect, not to separate close candidates. %d rows makes it a benchmark.",
			humanRows, MinHumanRowsForBenchmark)
	default:
		return fmt.Sprintf("%d human-labelled rows — benchmark grade.", humanRows)
	}
}

// JSON renders the report as indented, machine-readable JSON.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Table renders the report as an aligned, human-readable table.
//
// The caveat is printed BEFORE the table, not as a footnote: on an
// under-powered corpus the rate columns render as "—" so there is no confident
// percentage to skim past in the first place.
func (r Report) Table() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Keeper governance-model replay — incumbent=%s, tolerance=%.3f\n",
		r.Incumbent, r.Tolerance)
	fmt.Fprintf(&sb, "Corpus: %d human-labelled, %d incumbent-labelled — %s\n",
		r.HumanRows, r.IncumbentLabelRows, r.Strength)
	fmt.Fprintf(&sb, "%s\n\n", wrap(r.Caveat, 78))

	quotable := r.Strength.Quotable()
	tw := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MODEL\tDANGER_FLIP\tΔFLIP\tAGREE(human)\tΔAGREE\tRISK_MAE\tvs-INCUMBENT\tPASSES\tVERDICT")
	for _, c := range r.Candidates {
		label := c.Label
		if c.IsIncumbent {
			label += " (incumbent)"
		}

		flip, agree := "—", "—"
		if quotable {
			flip = fmt.Sprintf("%.3f (%d/%d)", c.DangerousFlipRate, c.DangerousFlipRows, c.HumanRows)
			agree = fmt.Sprintf("%.3f [%.2f–%.2f]", c.AgreementRate, c.AgreementLow, c.AgreementHigh)
		}
		flipDelta, agreeDelta := "—", "—"
		if quotable && !c.IsIncumbent {
			flipDelta = fmt.Sprintf("%+.3f", c.DangerousFlipDelta)
			agreeDelta = fmt.Sprintf("%+.3f", c.AgreementDelta)
		}
		riskMAE := "—"
		if quotable {
			riskMAE = fmt.Sprintf("%.2f", c.RiskMAE)
		}
		// Always shown, always named for what it is: this column has data even
		// when the human segment is empty, and mislabelling it as accuracy is the
		// original defect.
		vsIncumbent := fmt.Sprintf("%.3f (%d)", c.IncumbentLabelAgreement, c.IncumbentLabelRows)

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			label, flip, flipDelta, agree, agreeDelta, riskMAE, vsIncumbent, c.Passes,
			verdictMark(c))
	}
	tw.Flush()
	return sb.String()
}

// verdictMark renders the adoption gate. "NO: <reason>" rather than a bare NO —
// "the corpus is too small" and "it downgrades guards" call for opposite
// responses from the operator, and a single flag cannot tell them apart.
func verdictMark(c RankedCandidate) string {
	switch {
	case c.Viable && c.IsIncumbent:
		return "baseline"
	case c.Viable:
		return "yes"
	default:
		return "NO: " + c.Blocker
	}
}

// wrap breaks text on spaces at width columns so a long caveat stays readable in
// a terminal instead of running off the right edge where it goes unread.
func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var sb strings.Builder
	lineLen := 0
	for i, w := range words {
		switch {
		case i == 0:
			sb.WriteString(w)
			lineLen = len(w)
		case lineLen+1+len(w) > width:
			sb.WriteString("\n")
			sb.WriteString(w)
			lineLen = len(w)
		default:
			sb.WriteString(" ")
			sb.WriteString(w)
			lineLen += 1 + len(w)
		}
	}
	return sb.String()
}
