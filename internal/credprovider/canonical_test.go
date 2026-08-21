package credprovider

import "testing"

// Canonical folds a recognized provider onto its canonical spelling and leaves
// everything else alone. The asymmetry is load-bearing in both directions:
// without the folding, "openai_compat" from the dashboard skipped the endpoint
// gate, the delivery-time value split and the sidecar's CredStore, silently;
// with blanket upper-casing, an operator's free-form "MyInternalVault" would be
// rewritten into a label they never typed, in a column that has never been a
// closed enum.
func TestCanonical(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"OPENAI_COMPAT", "OPENAI_COMPAT"},
		{"openai_compat", "OPENAI_COMPAT"},
		{"OpenAI_Compat", "OPENAI_COMPAT"},
		{"  openrouter  ", "OPENROUTER"},
		{"github", "GITHUB"},
		{"", ""},
		{"   ", ""},
		// Unrecognized: trimmed, never re-cased.
		{"MyInternalVault", "MyInternalVault"},
		{"  MyInternalVault  ", "MyInternalVault"},
		{"acme-corp", "acme-corp"},
	}
	for _, tt := range tests {
		if got := Canonical(tt.in); got != tt.want {
			t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Every entry in Providers must already BE canonical, or Canonical would fold a
// provider onto a spelling the rest of the codebase does not use.
func TestProvidersAreSelfCanonical(t *testing.T) {
	for _, p := range Providers {
		if got := Canonical(p); got != p {
			t.Errorf("Canonical(%q) = %q: the list is not self-canonical", p, got)
		}
	}
}
