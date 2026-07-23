package api

import "testing"

// Adversarial unit test for #1364 R2. withholdKeeperSecretValues is now the
// SOLE place that blanks SECRET plaintext — buildKeeperBlock is read-only (it
// only lists the Keeper-gated credential names for the prompt, it no longer
// mutates values). This test exercises the withholding function in isolation so
// the explicit chokepoint is proven to work on its own: nothing else in the
// resolve path will paper over a regression here, so if this function ever
// stops blanking, that gap surfaces directly instead of being masked.
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
