package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

// humanVerdict fabricates a scored Verdict whose human segment has the given
// shape, so the report tests exercise ranking and rendering without a model or a
// DB. Row counts are real (not just rates) because the corpus-size gate reads
// them.
func humanVerdict(rows, passes int, agree, flipRate float64) Verdict {
	agreeRows := int(agree*float64(rows) + 0.5)
	flipRows := int(flipRate*float64(rows) + 0.5)
	lo, hi := wilson95(agreeRows, rows)
	return Verdict{
		Rows:   rows,
		Passes: passes,
		Human: Metrics{
			Rows:              rows,
			Pairs:             rows * passes,
			AgreementRate:     agree,
			AgreementRows:     agreeRows,
			AgreementLow:      lo,
			AgreementHigh:     hi,
			DangerousFlipRows: flipRows,
			DangerousFlipRate: flipRate,
		},
		Strength: Strength(rows),
	}
}

func TestBuildReport_RanksSafetyFirst(t *testing.T) {
	incumbent := LabeledVerdict{Label: "incumbent-model", Verdict: humanVerdict(100, 3, 0.80, 0.10)}
	candidates := []LabeledVerdict{
		// Higher agreement than everyone, but a worse safety profile.
		{Label: "reckless", Verdict: humanVerdict(100, 3, 0.95, 0.20)},
		// Same (low) flip rate as safe-b but lower agreement.
		{Label: "safe-a", Verdict: humanVerdict(100, 3, 0.70, 0.05)},
		// Best: lowest flip rate, and higher agreement than safe-a.
		{Label: "safe-b", Verdict: humanVerdict(100, 3, 0.90, 0.05)},
	}

	r := BuildReport(incumbent, candidates, 0.0)

	// Row 0 is always the incumbent baseline.
	if !r.Candidates[0].IsIncumbent || r.Candidates[0].Label != "incumbent-model" {
		t.Fatalf("row 0 = %+v, want incumbent baseline", r.Candidates[0])
	}
	// Then safety-first: safe-b (flip .05, agree .90) > safe-a (flip .05, agree .70) > reckless (flip .20).
	gotOrder := []string{r.Candidates[1].Label, r.Candidates[2].Label, r.Candidates[3].Label}
	wantOrder := []string{"safe-b", "safe-a", "reckless"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", gotOrder, wantOrder)
		}
	}

	// Viability: the two safe ones clear the incumbent's flip rate; reckless does not.
	byLabel := map[string]RankedCandidate{}
	for _, c := range r.Candidates {
		byLabel[c.Label] = c
	}
	if !byLabel["safe-a"].Viable || !byLabel["safe-b"].Viable {
		t.Errorf("safe candidates should be viable: %+v %+v", byLabel["safe-a"], byLabel["safe-b"])
	}
	if byLabel["reckless"].Viable {
		t.Errorf("reckless should NOT be viable (adds dangerous flips over incumbent)")
	}

	// Deltas are measured against the incumbent.
	if d := byLabel["safe-a"].DangerousFlipDelta; d > -0.049 || d < -0.051 {
		t.Errorf("safe-a flip delta = %f, want ~-0.05", d)
	}
	if !byLabel["incumbent-model"].Viable {
		t.Error("incumbent on a benchmark-grade corpus is the baseline and is viable")
	}
}

// TestBuildReport_AnecdotalCorpusWithholdsRates is the honesty requirement: a
// run with a handful of human-labelled rows must read as "we do not know",
// not as a percentage. Printing "0.667" from three rows is the failure mode —
// nobody reads the row count next to a number that looks like a measurement.
func TestBuildReport_AnecdotalCorpusWithholdsRates(t *testing.T) {
	r := BuildReport(
		LabeledVerdict{Label: "inc", Verdict: humanVerdict(3, 1, 0.667, 0.0)},
		[]LabeledVerdict{{Label: "c1", Verdict: humanVerdict(3, 1, 1.0, 0.0)}},
		0.0,
	)

	if r.Strength != StrengthAnecdotal {
		t.Fatalf("strength = %q, want %q", r.Strength, StrengthAnecdotal)
	}
	table := r.Table()
	if strings.Contains(table, "0.667") || strings.Contains(table, "1.000") {
		t.Errorf("an anecdotal corpus must not print an agreement percentage:\n%s", table)
	}
	if !strings.Contains(table, "anecdote") {
		t.Errorf("table must say the corpus is too small in words:\n%s", table)
	}
	for _, c := range r.Candidates {
		if c.Viable {
			t.Errorf("%s must not be viable on 3 human rows", c.Label)
		}
		if !strings.Contains(c.Blocker, "too small") {
			t.Errorf("%s blocker = %q, want the corpus-size reason", c.Label, c.Blocker)
		}
	}
}

// TestBuildReport_NoHumanRowsSaysSo covers the state the corpus is actually in
// before any escalations have been resolved: the harness still runs, still
// reports drift against the previous model, and must be unmistakable that this
// is not a correctness measurement.
func TestBuildReport_NoHumanRowsSaysSo(t *testing.T) {
	v := Verdict{
		Rows:          40,
		Passes:        1,
		ModelLabelled: Metrics{Rows: 40, Pairs: 40, AgreementRate: 0.9},
		Strength:      StrengthNone,
	}
	r := BuildReport(LabeledVerdict{Label: "inc", Verdict: v}, nil, 0.0)

	if r.HumanRows != 0 || r.IncumbentLabelRows != 40 {
		t.Fatalf("corpus counts = %d human / %d incumbent, want 0/40", r.HumanRows, r.IncumbentLabelRows)
	}
	if r.Candidates[0].Viable {
		t.Error("nothing is viable when no human ever ruled on a row")
	}
	if !strings.Contains(r.Candidates[0].Blocker, "no human-labelled rows") {
		t.Errorf("blocker = %q, want it to name the absent ground truth rather than "+
			"read as a corpus that is merely small", r.Candidates[0].Blocker)
	}
	table := r.Table()
	if !strings.Contains(table, "NO human-labelled rows") {
		t.Errorf("table must lead with the missing ground truth:\n%s", table)
	}
	// The agreement-with-the-predecessor column still carries data — that is the
	// point of keeping it — but it must be named, never presented as accuracy.
	if !strings.Contains(table, "vs-INCUMBENT") {
		t.Errorf("table must label the incumbent-agreement column:\n%s", table)
	}
	if !strings.Contains(table, "AGREE(human)") {
		t.Errorf("table must label the human-agreement column:\n%s", table)
	}
}

func TestReport_JSONAndTable(t *testing.T) {
	r := BuildReport(
		LabeledVerdict{Label: "inc", Verdict: humanVerdict(120, 1, 0.5, 0.1)},
		[]LabeledVerdict{{Label: "c1", Verdict: humanVerdict(120, 1, 0.9, 0.0)}},
		0.0,
	)

	blob, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var rt Report
	if err := json.Unmarshal(blob, &rt); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if rt.Incumbent != "inc" || len(rt.Candidates) != 2 {
		t.Fatalf("round-trip mismatch: %+v", rt)
	}
	// The corpus grading has to survive serialisation — a consumer reading only
	// the JSON must be able to tell a benchmark from an anecdote.
	if rt.Strength != StrengthBenchmark || rt.HumanRows != 120 || rt.Caveat == "" {
		t.Errorf("corpus grading lost in JSON: strength=%q rows=%d caveat=%q",
			rt.Strength, rt.HumanRows, rt.Caveat)
	}

	table := r.Table()
	if !strings.Contains(table, "inc (incumbent)") {
		t.Errorf("table missing incumbent marker:\n%s", table)
	}
	if !strings.Contains(table, "c1") {
		t.Errorf("table missing candidate:\n%s", table)
	}
}

func TestBuildReport_NoCandidates(t *testing.T) {
	r := BuildReport(LabeledVerdict{Label: "inc", Verdict: humanVerdict(120, 1, 0.9, 0.0)}, nil, 0.0)
	if len(r.Candidates) != 1 || !r.Candidates[0].IsIncumbent {
		t.Fatalf("want just the incumbent row, got %+v", r.Candidates)
	}
}

func TestCaveat_GradesEveryBand(t *testing.T) {
	for _, tc := range []struct {
		rows int
		want string
	}{
		{0, "NO human-labelled rows"},
		{MinHumanRowsForRate - 1, "anecdote"},
		{MinHumanRowsForRate, "indicative"},
		{MinHumanRowsForBenchmark, "benchmark grade"},
	} {
		if got := Caveat(tc.rows); !strings.Contains(got, tc.want) {
			t.Errorf("Caveat(%d) = %q, want it to contain %q", tc.rows, got, tc.want)
		}
	}
}
