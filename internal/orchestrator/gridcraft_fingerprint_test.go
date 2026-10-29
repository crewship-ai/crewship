package orchestrator

import "testing"

func TestGridCraftSwitchPreservesMissingCredentialBinding(t *testing.T) {
	credentials := []Credential{{ID: "provider", EnvVarName: "OPENROUTER_API_KEY", Type: "API_KEY", Provider: "OPENROUTER", PlainValue: "synthetic"}}
	missing := gridcraftSidecarFingerprint("", credentials, true)
	if missing != "" || !credentialIsolationFailedOpen(missing, credentials) {
		t.Fatal("workbook switch invented a credential binding without a signing key")
	}
	base := sidecarConfigFingerprint("synthetic-signing-key", credentials)
	if got := gridcraftSidecarFingerprint("synthetic-signing-key", credentials, false); got != base {
		t.Fatal("default-off switch changed existing credential identity")
	}
	if got := gridcraftSidecarFingerprint("synthetic-signing-key", credentials, true); got == "" || got == base {
		t.Fatal("enabled workbook tools did not invalidate the old sidecar")
	}
}
