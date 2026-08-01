package credname

import "testing"

// TestCanonical_FoldsTheTwoWaysANameDiffersFromAVariable pins the whole
// mapping, in both directions. The positive half is the fix for #1657 —
// `github-token` has to become a variable an agent can read, or the
// normalisation buys nothing. The negative half is why the mapping stops
// there: everything it refuses is a name we would otherwise have to INVENT,
// and an invented name is one the operator cannot predict.
func TestCanonical_FoldsTheTwoWaysANameDiffersFromAVariable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		// Case and dashes: the conventions a credential NAME follows.
		{"github-token", "GITHUB_TOKEN", true},
		{"gh_token", "GH_TOKEN", true},
		{"Github-Acme", "GITHUB_ACME", true},
		{"aws-key-2", "AWS_KEY_2", true},
		{"_private", "_PRIVATE", true},
		// Already a variable: must round-trip byte-identical, or "the operator
		// named a variable" stops being detectable.
		{"GH_TOKEN", "GH_TOKEN", true},
		{"_OAUTH", "_OAUTH", true},
		// Refusals: each would need a name invented for it.
		{"", "", false},
		{"gh token", "", false},
		{"_OAUTH_ACCESS_TOKEN:c123", "", false},
		{"2fa-token", "", false},
		{"GH;rm -rf /", "", false},
		{"héslo", "", false},
		{"straße", "", false},
	}
	for _, c := range cases {
		got, ok := Canonical(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Canonical(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestCanonical_OutputIsAlwaysValid is the property that makes one rule out of
// two functions: the writer's gate and the reader's gate cannot drift, because
// anything Canonical accepts it also renders into something Valid accepts.
// Without this a Canonical that (say) let a leading digit through would produce
// names the orchestrator refuses to write, which is the original defect wearing
// a new hat.
func TestCanonical_OutputIsAlwaysValid(t *testing.T) {
	t.Parallel()
	// Every byte, alone and in second position, so the leading-digit rule and
	// the general charset rule are both exercised.
	for b := 0; b < 256; b++ {
		for _, in := range []string{string(rune(b)), "A" + string(rune(b))} {
			got, ok := Canonical(in)
			if !ok {
				continue
			}
			if !Valid(got) {
				t.Fatalf("Canonical(%q) = %q, which Valid rejects — the two rules disagree", in, got)
			}
		}
	}
}

// TestValid_IsUppercaseOnly guards the reader's rule itself. A lowercase name
// reaching a container is not a cosmetic problem: /secrets/<agent>/gh_token and
// /secrets/<agent>/GH_TOKEN are two different files on a case-sensitive
// filesystem, and the revoke reconciler removes exactly one of them.
func TestValid_IsUppercaseOnly(t *testing.T) {
	t.Parallel()
	valid := []string{"GH_TOKEN", "_X", "A1", "___"}
	invalid := []string{"", "gh_token", "GH-TOKEN", "1GH", "GH TOKEN", "GH.TOKEN"}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if Valid(s) {
			t.Errorf("Valid(%q) = true, want false", s)
		}
	}
}
