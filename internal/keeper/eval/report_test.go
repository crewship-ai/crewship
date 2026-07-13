package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildReport_RanksSafetyFirst(t *testing.T) {
	incumbent := LabeledVerdict{
		Label:   "incumbent-model",
		Verdict: Verdict{Rows: 100, Passes: 3, AgreementRate: 0.80, DangerousFlipRate: 0.10, DangerousFlipRows: 10},
	}
	candidates := []LabeledVerdict{
		// Higher agreement than everyone, but a worse safety profile.
		{Label: "reckless", Verdict: Verdict{Rows: 100, Passes: 3, AgreementRate: 0.95, DangerousFlipRate: 0.20, DangerousFlipRows: 20}},
		// Same (low) flip rate as safe-b but lower agreement.
		{Label: "safe-a", Verdict: Verdict{Rows: 100, Passes: 3, AgreementRate: 0.70, DangerousFlipRate: 0.05, DangerousFlipRows: 5}},
		// Best: lowest flip rate, and higher agreement than safe-a.
		{Label: "safe-b", Verdict: Verdict{Rows: 100, Passes: 3, AgreementRate: 0.90, DangerousFlipRate: 0.05, DangerousFlipRows: 5}},
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
		t.Error("incumbent is always viable (it is the baseline)")
	}
}

func TestReport_JSONAndTable(t *testing.T) {
	r := BuildReport(
		LabeledVerdict{Label: "inc", Verdict: Verdict{Rows: 10, Passes: 1, AgreementRate: 0.5, DangerousFlipRate: 0.1}},
		[]LabeledVerdict{{Label: "c1", Verdict: Verdict{Rows: 10, Passes: 1, AgreementRate: 0.9, DangerousFlipRate: 0.0}}},
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

	table := r.Table()
	if !strings.Contains(table, "inc (incumbent)") {
		t.Errorf("table missing incumbent marker:\n%s", table)
	}
	if !strings.Contains(table, "c1") {
		t.Errorf("table missing candidate:\n%s", table)
	}
}

func TestBuildReport_NoCandidates(t *testing.T) {
	r := BuildReport(LabeledVerdict{Label: "inc", Verdict: Verdict{Rows: 5}}, nil, 0.0)
	if len(r.Candidates) != 1 || !r.Candidates[0].IsIncumbent {
		t.Fatalf("want just the incumbent row, got %+v", r.Candidates)
	}
}
