package api

import "testing"

// Adversarial unit test for #1364 R2. The resolve-response integration tests
// pass even if withholdKeeperSecretValues were a no-op, because buildKeeperBlock
// ALSO blanks SECRET values as a side effect. This test exercises the function
// in isolation so the explicit chokepoint is proven to work on its own — the
// whole point of the defense-in-depth: if a refactor removes buildKeeperBlock's
// side effect, this function must still hold the line.
func TestWithholdKeeperSecretValues_BlanksOnlySecret(t *testing.T) {
	creds := []mcpCredEntry{
		{ID: "1", EnvVar: "PROD_KEY", Value: "secret-plaintext", Type: "SECRET"},
		{ID: "2", EnvVar: "STRIPE_HOOK", Value: "generic-plaintext", Type: "GENERIC_SECRET"},
		{ID: "3", EnvVar: "GH_TOKEN", Value: "cli-plaintext", Type: "CLI_TOKEN"},
		{ID: "4", EnvVar: "ANTHROPIC", Value: "api-plaintext", Type: "API_KEY"},
		{ID: "5", EnvVar: "OAUTH", Value: "oauth-plaintext", Type: "OAUTH2"},
	}

	withholdKeeperSecretValues(creds)

	if creds[0].Value != "" {
		t.Errorf("SECRET value must be blanked, got %q", creds[0].Value)
	}
	// Everything that is NOT SECRET must be delivered untouched — the gate is
	// SECRET-only by design (see credentials.mdx / buildKeeperBlock).
	for _, i := range []int{1, 2, 3, 4} {
		if creds[i].Value == "" {
			t.Errorf("%s value must be preserved, but it was blanked", creds[i].Type)
		}
	}
}

// A slice with no SECRET is left entirely untouched (no accidental broad wipe).
func TestWithholdKeeperSecretValues_NoSecretIsNoop(t *testing.T) {
	creds := []mcpCredEntry{
		{ID: "1", EnvVar: "GH_TOKEN", Value: "cli-plaintext", Type: "CLI_TOKEN"},
		{ID: "2", EnvVar: "STRIPE_HOOK", Value: "generic-plaintext", Type: "GENERIC_SECRET"},
	}
	withholdKeeperSecretValues(creds)
	if creds[0].Value != "cli-plaintext" || creds[1].Value != "generic-plaintext" {
		t.Errorf("non-SECRET creds must be untouched, got %q / %q", creds[0].Value, creds[1].Value)
	}
}
