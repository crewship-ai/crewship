package main

import "testing"

func TestLooksLikeCUID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		// CUID2 minimum: "c" + 20 alphanumeric = 21 chars.
		{"exactly 21 chars — cuid2 minimum", "cabcdefghijklmnopqrst", true},
		{"24-char cuid2 default", "ckl4f7g3i0001p2k8a9d3v5x7", true},
		{"30-char cuid", "ckl4f7g3i0001p2k8a9d3v5x7yza1bc", true},

		// Off-by-one — earlier bug accepted these.
		{"exactly 20 chars — too short", "cabcdefghijklmnopqrs", false},
		{"19 chars", "cabcdefghijklmnopqr", false},

		{"empty", "", false},
		{"single c", "c", false},

		// Must start with lowercase 'c'.
		{"uppercase prefix", "Cabcdefghijklmnopqrst", false},
		{"non-c prefix", "babcdefghijklmnopqrst", false},
		{"digit prefix", "1abcdefghijklmnopqrst", false},

		// Must be lowercase alphanumeric throughout.
		{"uppercase middle", "cABCDEFGHIJKLMNOPQRST", false},
		{"hyphen middle", "cabcd-fghijklmnopqrst", false},
		{"underscore middle", "cabcd_fghijklmnopqrst", false},
		{"space inside", "cabcd fghijklmnopqrst", false},

		// Realistic-looking slugs that previously could collide with the
		// short minimum — none should be misclassified now.
		{"long slug with hyphens", "crewship-orchestration", false},
		{"slug 'customer-success'", "customer-success-team", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeCUID(tt.in); got != tt.want {
				t.Errorf("looksLikeCUID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
