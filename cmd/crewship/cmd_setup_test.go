package main

import "testing"

// TestParseYesNo pins the consent-prompt parsing contract: empty input
// accepts the default, y/yes and n/no are case-insensitive, and anything
// else is rejected (ok=false) so promptYesNo re-asks instead of silently
// recording a consent value the user didn't give.
func TestParseYesNo(t *testing.T) {
	cases := []struct {
		raw     string
		def     bool
		wantVal bool
		wantOK  bool
	}{
		{"", true, true, true},
		{"", false, false, true},
		{"  ", true, true, true}, // whitespace = bare Enter
		{"y", false, true, true},
		{"Y", false, true, true},
		{"yes", false, true, true},
		{"YES", false, true, true},
		{"n", true, false, true},
		{"N", true, false, true},
		{"no", true, false, true},
		{"No", true, false, true},
		{"sure", false, false, false},
		{"0", true, false, false},
		{"j", false, false, false},
	}
	for _, tc := range cases {
		val, ok := parseYesNo(tc.raw, tc.def)
		if ok != tc.wantOK || (ok && val != tc.wantVal) {
			t.Errorf("parseYesNo(%q, def=%v) = (%v, %v), want (%v, %v)",
				tc.raw, tc.def, val, ok, tc.wantVal, tc.wantOK)
		}
	}
}
