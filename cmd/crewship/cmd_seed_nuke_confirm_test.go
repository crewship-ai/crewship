package main

import "testing"

// TestNukeDecision pins the confirmation policy for `seed --nuke`. The wipe is
// the single most destructive CLI action (it deletes ALL workspace contents),
// so the gate must be strict: --yes bypasses (CI), a non-interactive session
// without --yes is refused outright, and an interactive run requires the
// operator to type the exact workspace slug.
func TestNukeDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		yes         bool
		interactive bool
		typed       string
		wsSlug      string
		wantErr     bool
	}{
		{"--yes bypasses everything", true, false, "", "acme", false},
		{"--yes bypasses even unknown slug", true, false, "", "", false},
		{"non-interactive without --yes is refused", false, false, "acme", "acme", true},
		{"interactive correct slug proceeds", false, true, "acme", "acme", false},
		{"interactive trims whitespace/newline", false, true, "  acme \n", "acme", false},
		{"interactive wrong slug aborts", false, true, "acme-prod", "acme", true},
		{"interactive empty input aborts", false, true, "", "acme", true},
		{"unknown slug never matches even if typed empty", false, true, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := nukeDecision(tc.yes, tc.interactive, tc.typed, tc.wsSlug)
			if (err != nil) != tc.wantErr {
				t.Fatalf("nukeDecision(yes=%v, interactive=%v, typed=%q, slug=%q) err=%v, wantErr=%v",
					tc.yes, tc.interactive, tc.typed, tc.wsSlug, err, tc.wantErr)
			}
		})
	}
}
