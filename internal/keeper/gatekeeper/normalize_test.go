package gatekeeper

import "testing"

// TestNormalizeRawResponse locks the fail-closed parse rules shared by the live
// Evaluate path and the M2a replay eval driver. If these change, both must
// change together — that is the whole point of the shared helper.
func TestNormalizeRawResponse(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantDec    string
		wantRisk   int
		wantReason string
		wantErr    bool
	}{
		{
			name:       "valid allow lowercased",
			raw:        `{"decision":"allow","reason":"looks fine","risk":3}`,
			wantDec:    "ALLOW",
			wantRisk:   3,
			wantReason: "looks fine",
		},
		{
			name:     "escalate embedded in surrounding text",
			raw:      "Sure! Here you go: {\"decision\":\"ESCALATE\",\"risk\":7} — done.",
			wantDec:  "ESCALATE",
			wantRisk: 7,
		},
		{
			name:     "unknown decision falls back to DENY",
			raw:      `{"decision":"maybe","risk":5}`,
			wantDec:  "DENY",
			wantRisk: 5,
		},
		{
			name:     "risk above range clamped to 10",
			raw:      `{"decision":"allow","risk":50}`,
			wantDec:  "ALLOW",
			wantRisk: 10,
		},
		{
			name:     "risk below range clamped to 1",
			raw:      `{"decision":"deny","risk":0}`,
			wantDec:  "DENY",
			wantRisk: 1,
		},
		{
			name:     "unparseable is fail-closed DENY at risk 10 with error",
			raw:      "the model refused and returned prose only",
			wantDec:  "DENY",
			wantRisk: 10,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, risk, reason, err := NormalizeRawResponse(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if dec != tt.wantDec {
				t.Errorf("decision = %q, want %q", dec, tt.wantDec)
			}
			if risk != tt.wantRisk {
				t.Errorf("risk = %d, want %d", risk, tt.wantRisk)
			}
			if tt.wantReason != "" && reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
