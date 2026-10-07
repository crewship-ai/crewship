package keeper

import "testing"

func TestRefusalRiskUsesTierFloor(t *testing.T) {
	for _, tc := range []struct{ floor, want int }{{0, 3}, {-1, 3}, {1, 1}, {7, 7}, {10, 10}} {
		if got := (TierPolicy{MinRisk: tc.floor}).RefusalRisk(); got != tc.want {
			t.Errorf("floor %d: refusal risk %d, want %d", tc.floor, got, tc.want)
		}
	}
	for _, level := range SecurityLevels() {
		policy := level.Tier()
		if policy.RefusalRisk() < policy.MinRisk {
			t.Errorf("%s refusal below tier floor", level)
		}
	}
}
