package seeddata

import (
	"regexp"
	"strings"
	"testing"
)

// The demo vault ships with the product. These tests are about what it must
// never contain and never imply, not about how many rows it has.

// TestDemoCredentials_ValuesAreObviouslyFake is the one that matters. A demo
// value that LOOKS like a token gets treated as one: somebody tries it, somebody
// screenshots the page and it reads as a leak, and somebody eventually copies
// the shape into a real credential. Every value has to announce itself.
func TestDemoCredentials_ValuesAreObviouslyFake(t *testing.T) {
	// Prefixes real tokens actually use. A demo value must not begin with any
	// of them — that is the shape a scanner and a human both react to.
	realShapes := []string{
		"sk-", "sk-ant-", "ghp_", "github_pat_", "gho_", "glpat-",
		"AKIA", "ASIA", "xoxb-", "xoxp-", "npm_", "SG.", "rk_live", "sk_live",
	}
	for _, dc := range DemoCredentials() {
		v := dc.Def.Value
		if v == "" {
			t.Errorf("%s: empty value", dc.Def.Name)
			continue
		}
		// PEM blobs announce themselves by their envelope and carry the marker
		// inside the base64 body instead.
		isPEM := strings.HasPrefix(v, "-----BEGIN")
		if !isPEM && !strings.Contains(v, dummyPrefix) {
			t.Errorf("%s: value does not contain %q — a demo secret must say so in the value itself, "+
				"not only in its description", dc.Def.Name, dummyPrefix)
		}
		for _, shape := range realShapes {
			if strings.HasPrefix(v, shape) {
				t.Errorf("%s: value starts with %q, the prefix of a real credential — "+
					"someone will try it, and a screenshot of it reads as a leak", dc.Def.Name, shape)
			}
		}
	}
}

// TestDemoCredentials_SecretFieldsAreAlsoFake — a part is as much a secret as
// the primary value, and is just as visible in a screenshot.
func TestDemoCredentials_SecretFieldsAreAlsoFake(t *testing.T) {
	for _, dc := range DemoCredentials() {
		for _, f := range dc.Fields {
			if f.Secret && !strings.Contains(f.Value, dummyPrefix) {
				t.Errorf("%s field %s: secret part must be visibly fake", dc.Def.Name, f.Key)
			}
		}
	}
}

// TestDemoCredentials_CoverEveryShape pins the point of the set. Losing a type
// here is silent: the page still renders, the filter just quietly stops having
// anything to filter, which is exactly the state this data exists to fix.
func TestDemoCredentials_CoverEveryShape(t *testing.T) {
	want := []string{
		"CLI_TOKEN", "SECRET", "USERPASS", "SSH_KEY",
		"CERTIFICATE", "API_KEY", "GENERIC_SECRET",
	}
	seen := map[string]bool{}
	for _, dc := range DemoCredentials() {
		seen[dc.Def.Type] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("no demo credential of type %s — the type filter has nothing to show for it", w)
		}
	}
}

// TestDemoCredentials_ShowBothClassifications keeps the reveal story visible.
// SEALED in particular is a refusal people should meet in a demo rather than
// discover the first time they need a production secret.
func TestDemoCredentials_ShowBothClassifications(t *testing.T) {
	seen := map[string]bool{}
	for _, dc := range DemoCredentials() {
		if dc.Sensitivity != "" {
			seen[dc.Sensitivity] = true
		}
	}
	for _, w := range []string{"RESTRICTED", "SEALED"} {
		if !seen[w] {
			t.Errorf("no demo credential classified %s — the badge and its refusal never appear", w)
		}
	}
}

// TestDemoCredentials_MultiAccountIsDemonstrated — two accounts of one provider
// under one slot is the case that was impossible before bindings, and a demo
// that does not show it leaves the model as a claim in a document.
func TestDemoCredentials_MultiAccountIsDemonstrated(t *testing.T) {
	byProvider := map[string]int{}
	for _, dc := range DemoCredentials() {
		byProvider[dc.Def.Provider]++
	}
	if byProvider["GITHUB"] < 2 {
		t.Error("fewer than two GitHub accounts: the multi-account case is not demonstrated")
	}

	slots := map[string]int{}
	crews := map[string]bool{}
	for _, b := range DemoBindings() {
		slots[b.Slot]++
		if crews[b.CrewSlug] {
			t.Errorf("two demo bindings target crew %s — one slot per scope, so the second would 409",
				b.CrewSlug)
		}
		crews[b.CrewSlug] = true
	}
	if slots["GH_TOKEN"] < 2 {
		t.Error("GH_TOKEN is not bound in two crews: the point is that one slot reaches two accounts")
	}
}

// TestDemoCredentials_NamesAreDistinctAndValid — the seed resolves by name and
// the workspace enforces uniqueness, so a duplicate would make one row silently
// win and the other look like a failure.
func TestDemoCredentials_NamesAreDistinctAndValid(t *testing.T) {
	nameRE := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	seen := map[string]bool{}
	for _, dc := range DemoCredentials() {
		if seen[dc.Def.Name] {
			t.Errorf("duplicate demo credential name %q", dc.Def.Name)
		}
		seen[dc.Def.Name] = true
		if !nameRE.MatchString(dc.Def.Name) {
			t.Errorf("name %q is not a plain identifier", dc.Def.Name)
		}
	}
	for _, b := range DemoBindings() {
		if !seen[b.CredentialName] {
			t.Errorf("binding references %q, which is not in the demo set", b.CredentialName)
		}
	}
}

// TestDemoCredentials_UserPassCarriesItsUsername — the server rejects a
// USERPASS with no username, and the seed reports the failure and moves on, so
// the row would simply be absent with only a line in the log to say why.
func TestDemoCredentials_UserPassCarriesItsUsername(t *testing.T) {
	for _, dc := range DemoCredentials() {
		if dc.Def.Type == "USERPASS" && dc.Username == "" {
			t.Errorf("%s is USERPASS with no username — the server will refuse it", dc.Def.Name)
		}
	}
}

// TestDemoCredentials_PEMShapesAreWellFormed — SSH_KEY and CERTIFICATE are
// validated by envelope, and a malformed one is refused with a message about
// PEM that says nothing about the seed.
func TestDemoCredentials_PEMShapesAreWellFormed(t *testing.T) {
	for _, dc := range DemoCredentials() {
		switch dc.Def.Type {
		case "SSH_KEY":
			if !strings.Contains(dc.Def.Value, "-----BEGIN") ||
				!strings.Contains(dc.Def.Value, "PRIVATE KEY-----") {
				t.Errorf("%s: SSH_KEY needs a PEM private-key envelope", dc.Def.Name)
			}
		case "CERTIFICATE":
			if !strings.Contains(dc.Def.Value, "-----BEGIN CERTIFICATE-----") ||
				!strings.Contains(dc.Def.Value, "-----END CERTIFICATE-----") {
				t.Errorf("%s: CERTIFICATE needs a PEM certificate envelope", dc.Def.Name)
			}
		}
	}
}
