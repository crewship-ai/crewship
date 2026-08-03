package memory

import "testing"

func TestCapForPath(t *testing.T) {
	tests := []struct {
		rel    string
		want   int
		wantOK bool
	}{
		{"AGENT.md", capAgentBytes, true},
		{"CREW.md", capCrewBytes, true},
		{"PERSONA.md", capPersonaBytes, true},
		{"pins.md", capPinsBytes, true},
		{"daily/2026-08-01.md", capDailyBytes, true},
		{"peers/pavel.md", capPeerBytes, true},
		// Recognised but deliberately uncapped — distinct from unknown.
		{"lessons.md", 0, true},
		{"learned.md", 0, true},
		// Unknown paths must not inherit a neighbour's cap.
		{"daily/nested/x.md", 0, false},
		{"peers/.env", 0, false},
		{"daily/token", 0, false},
		{"secrets.md", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, ok := CapForPath(tt.rel)
			if ok != tt.wantOK {
				t.Fatalf("CapForPath(%q) ok = %v, want %v", tt.rel, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("CapForPath(%q) = %d, want %d", tt.rel, got, tt.want)
			}
		})
	}
}

// The path-keyed form must agree with the tier-keyed one it was
// extracted from. Two tables that disagree would let a write pass one
// ceiling and fail the other depending on which door it came through.
func TestCapForPathAgreesWithCapForTier(t *testing.T) {
	pairs := []struct{ rel, tier string }{
		{"AGENT.md", "AGENT"},
		{"CREW.md", "CREW"},
		{"PERSONA.md", "PERSONA"},
		{"pins.md", "pins"},
		{"daily/2026-08-01.md", "daily"},
		{"peers/pavel.md", "peers"},
		{"lessons.md", "lessons"},
	}
	for _, p := range pairs {
		byPath, ok := CapForPath(p.rel)
		if !ok {
			t.Errorf("CapForPath(%q) unrecognised", p.rel)
			continue
		}
		byTier, err := capForTier(p.tier)
		if err != nil {
			t.Errorf("capForTier(%q): %v", p.tier, err)
			continue
		}
		if byPath != byTier {
			t.Errorf("%s: CapForPath = %d but capForTier(%s) = %d", p.rel, byPath, p.tier, byTier)
		}
	}
}
