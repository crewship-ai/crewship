package main

import "testing"

// TestFindActiveWorkspace pins the fail-closed lookup: when the active
// workspace id doesn't match any row in the listing, the function MUST return
// ("", "") rather than fall back to the first workspace. The destructive nuke
// path relies on this — a wrong-but-non-empty slug would let an operator type
// it, confirm under a false identity, and still wipe the real active workspace
// (the wipe is wsCtx-bound server-side, not slug-bound). CodeRabbit caught this
// as a Major issue in review round 3 of PR #600.
func TestFindActiveWorkspace(t *testing.T) {
	t.Parallel()
	wss := []workspaceSummary{
		{ID: "ws-a", Name: "Acme", Slug: "acme"},
		{ID: "ws-b", Name: "Beta", Slug: "beta"},
	}
	cases := []struct {
		name           string
		wss            []workspaceSummary
		activeID       string
		wantName, want string
	}{
		{"match returns identity", wss, "ws-b", "Beta", "beta"},
		{"first-entry match", wss, "ws-a", "Acme", "acme"},
		{"no match returns empty", wss, "ws-missing", "", ""},
		{"empty list returns empty", nil, "ws-a", "", ""},
		{"empty active id returns empty (won't false-match an empty-id row)", wss, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, s := findActiveWorkspace(tc.wss, tc.activeID)
			if n != tc.wantName || s != tc.want {
				t.Errorf("findActiveWorkspace(_, %q) = (%q,%q); want (%q,%q)",
					tc.activeID, n, s, tc.wantName, tc.want)
			}
		})
	}
}

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
