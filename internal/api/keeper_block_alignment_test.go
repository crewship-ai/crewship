package api

import (
	"strings"
	"testing"
)

// TestBuildKeeperBlock_WithholdSetMatchesFileGate pins the prompt-vs-reality
// contract. The orchestrator file gate (exec_sidecar.go buildCredFileScript)
// withholds EXACTLY SECRET when Keeper is on; the env gate (exec_env.go
// BuildEnvVarsSidecar) does the same. The system-prompt block built here
// tells the agent "You do NOT have these credentials in your environment" —
// and it MUST make that claim for exactly the same type set, no more, no
// less. If the prompt listed a type the delivery paths still write as a file
// (e.g. GENERIC_SECRET or CLI_TOKEN), the agent would be told it lacks a
// credential that is in fact sitting in /secrets/<slug>/ — the same
// prompt-vs-reality bug class the SECRET fix set out to remove, re-appearing
// for a different type.
//
// This asserts buildKeeperBlock lists ONLY SECRET-typed creds and never the
// still-file-delivered types.
func TestBuildKeeperBlock_WithholdSetMatchesFileGate(t *testing.T) {
	h := covCfgHandler(nil)

	creds := []mcpCredEntry{
		{Type: "SECRET", EnvVar: "PROD_DB_PASSWORD", Value: "v1"},
		{Type: "CLI_TOKEN", EnvVar: "GH_TOKEN", Value: "v2"},         // still delivered as file → must NOT appear
		{Type: "GENERIC_SECRET", EnvVar: "STRIPE_HOOK", Value: "v3"}, // still delivered as file → must NOT appear
		{Type: "USERPASS", EnvVar: "VAULT", Value: "v4"},             // still delivered as file → must NOT appear
		{Type: "SSH_KEY", EnvVar: "DEPLOY", Value: "v5"},             // still delivered as file → must NOT appear
		{Type: "CERTIFICATE", EnvVar: "CA", Value: "v6"},             // still delivered as file → must NOT appear
		{Type: "API_KEY", EnvVar: "ANTHROPIC_API_KEY", Value: "v7"},  // sidecar-injected → must NOT appear
	}
	block := h.buildKeeperBlock("ada", creds)
	if block == "" {
		t.Fatal("expected a keeper block for a batch containing a SECRET")
	}

	// The one type the prompt claims withholding for must be listed.
	if !strings.Contains(block, "- PROD_DB_PASSWORD") {
		t.Errorf("keeper block must list the SECRET PROD_DB_PASSWORD; got:\n%s", block)
	}

	// Every type still delivered as a file (or via sidecar) must NOT be listed
	// under the "you do NOT have these" claim — otherwise the prompt lies.
	for _, name := range []string{"GH_TOKEN", "STRIPE_HOOK", "VAULT", "DEPLOY", "CA", "ANTHROPIC_API_KEY"} {
		if strings.Contains(block, "- "+name) {
			t.Errorf("keeper block must NOT claim withholding for still-delivered credential %q; "+
				"prompt/file-gate type sets are misaligned. Block:\n%s", name, block)
		}
	}
}

// TestBuildKeeperBlock_RedactsListedValues confirms the block never embeds a
// SECRET's cleartext value while advertising it, and that the resolver
// chokepoint (withholdKeeperSecretValues) wipes the entry so a later
// mcpCredEntry consumer can't reuse the plaintext of a credential the agent is
// being told it does not have. buildKeeperBlock itself is read-only now (#1364).
func TestBuildKeeperBlock_RedactsListedValues(t *testing.T) {
	h := covCfgHandler(nil)
	creds := []mcpCredEntry{
		{Type: "SECRET", EnvVar: "PROD_DB_PASSWORD", Value: "hunter2"},
	}
	block := h.buildKeeperBlock("ada", creds)
	if strings.Contains(block, "hunter2") {
		t.Errorf("keeper block must not contain the SECRET plaintext; got:\n%s", block)
	}
	withholdKeeperSecretValues(creds)
	if creds[0].Value != "" {
		t.Errorf("SECRET entry value must be wiped by withholdKeeperSecretValues, got %q", creds[0].Value)
	}
}
