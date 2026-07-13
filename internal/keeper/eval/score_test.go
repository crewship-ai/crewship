package eval

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScore_PerfectAgreement(t *testing.T) {
	rows := []Row{
		{Recorded: Allow, RecordedRisk: 1, Replays: []Replay{{Allow, 1}}},
		{Recorded: Deny, RecordedRisk: 9, Replays: []Replay{{Deny, 9}}},
		{Recorded: Escalate, RecordedRisk: 7, Replays: []Replay{{Escalate, 7}}},
	}
	v := Score(rows)
	if !approx(v.AgreementRate, 1.0) {
		t.Fatalf("agreement = %v, want 1.0", v.AgreementRate)
	}
	if v.DangerousFlipRows != 0 || !approx(v.DangerousFlipRate, 0) {
		t.Fatalf("expected no dangerous flips, got %d (%v)", v.DangerousFlipRows, v.DangerousFlipRate)
	}
	if !approx(v.RiskMAE, 0) {
		t.Fatalf("risk MAE = %v, want 0", v.RiskMAE)
	}
}

func TestScore_DangerousFlipCounted(t *testing.T) {
	// Recorded DENY downgraded to ALLOW is the safety-critical case.
	rows := []Row{
		{Recorded: Deny, RecordedRisk: 9, Replays: []Replay{{Allow, 2}}},
		{Recorded: Escalate, RecordedRisk: 7, Replays: []Replay{{Allow, 1}}},
		{Recorded: Allow, RecordedRisk: 1, Replays: []Replay{{Allow, 1}}},
	}
	v := Score(rows)
	if v.DangerousFlipRows != 2 {
		t.Fatalf("dangerous flip rows = %d, want 2", v.DangerousFlipRows)
	}
	if !approx(v.DangerousFlipRate, 2.0/3.0) {
		t.Fatalf("dangerous flip rate = %v, want 2/3", v.DangerousFlipRate)
	}
}

func TestScore_NonDangerousDisagreementIsNotAFlip(t *testing.T) {
	// Recorded ALLOW → replayed DENY lowers agreement but is NOT dangerous
	// (it's over-cautious, not a downgrade of a guard).
	rows := []Row{
		{Recorded: Allow, RecordedRisk: 1, Replays: []Replay{{Deny, 8}}},
	}
	v := Score(rows)
	if !approx(v.AgreementRate, 0) {
		t.Fatalf("agreement = %v, want 0", v.AgreementRate)
	}
	if v.DangerousFlipRows != 0 {
		t.Fatalf("over-caution must not count as a dangerous flip, got %d", v.DangerousFlipRows)
	}
}

func TestScore_WorstCaseAcrossPasses(t *testing.T) {
	// A row that flips in ANY pass counts as a dangerously-flipped row (safety
	// uses the worst case, not the mean). Agreement is the mean over (row,pass).
	rows := []Row{
		{Recorded: Deny, RecordedRisk: 9, Replays: []Replay{{Deny, 9}, {Allow, 2}, {Deny, 8}}},
	}
	v := Score(rows)
	if v.Passes != 3 {
		t.Fatalf("passes = %d, want 3", v.Passes)
	}
	if v.DangerousFlipRows != 1 {
		t.Fatalf("row flipping in 1/3 passes must count as flipped, got %d", v.DangerousFlipRows)
	}
	// Agreement: 2 of 3 passes agree with DENY → 2/3.
	if !approx(v.AgreementRate, 2.0/3.0) {
		t.Fatalf("agreement = %v, want 2/3", v.AgreementRate)
	}
}

func TestScore_RiskMAE(t *testing.T) {
	rows := []Row{
		{Recorded: Allow, RecordedRisk: 1, Replays: []Replay{{Allow, 3}}}, // err 2
		{Recorded: Deny, RecordedRisk: 10, Replays: []Replay{{Deny, 6}}},  // err 4
	}
	v := Score(rows)
	if !approx(v.RiskMAE, 3.0) {
		t.Fatalf("risk MAE = %v, want 3.0", v.RiskMAE)
	}
}

func TestScore_ConfusionMatrix(t *testing.T) {
	rows := []Row{
		{Recorded: Deny, Replays: []Replay{{Allow, 0}}},
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
		{Recorded: Allow, Replays: []Replay{{Allow, 0}}},
	}
	v := Score(rows)
	if v.Confusion[Deny][Allow] != 1 {
		t.Errorf("confusion[DENY][ALLOW] = %d, want 1", v.Confusion[Deny][Allow])
	}
	if v.Confusion[Deny][Deny] != 1 {
		t.Errorf("confusion[DENY][DENY] = %d, want 1", v.Confusion[Deny][Deny])
	}
	if v.Confusion[Allow][Allow] != 1 {
		t.Errorf("confusion[ALLOW][ALLOW] = %d, want 1", v.Confusion[Allow][Allow])
	}
}

func TestCompare_ViabilityGatesOnDangerousFlips(t *testing.T) {
	incumbent := Score([]Row{
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
	}) // 0 flips
	worse := Score([]Row{
		{Recorded: Deny, Replays: []Replay{{Allow, 0}}},
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
	}) // 0.5 flip rate
	better := Score([]Row{
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
		{Recorded: Deny, Replays: []Replay{{Deny, 0}}},
	}) // 0 flips

	if Compare(worse, incumbent).Viable(0.0) {
		t.Error("a candidate with MORE dangerous flips than incumbent must not be viable")
	}
	if !Compare(better, incumbent).Viable(0.0) {
		t.Error("a candidate matching the incumbent's flip rate must be viable at tolerance 0")
	}
	// Tolerance admits a small regression.
	if !Compare(worse, incumbent).Viable(0.5) {
		t.Error("tolerance 0.5 should admit a 0.5 flip-rate delta")
	}
}

func TestScore_EmptyCorpus(t *testing.T) {
	v := Score(nil)
	if v.Rows != 0 || v.AgreementRate != 0 || v.DangerousFlipRate != 0 {
		t.Fatalf("empty corpus must yield a zero verdict, got %+v", v)
	}
}
