package orchestrator

import (
	"strings"
	"testing"
)

// ── SECURITY FINDING (pre-existing, surfaced by this review) ─────────────────
//
// The "gate SECRET credential files on Keeper state" fix, and its docs
// (docs/guides/credentials.mdx), assert an absolute delivery contract:
//
//	"Keeper enabled — the SECRET is not written to the agent's filesystem at
//	 all, and it is not injected as an environment variable."
//
// The FILE path (buildCredFileScript) and the primary env path
// (BuildEnvVarsSidecar) both honour that. But there is a THIRD delivery path
// that does NOT: injectMCPCredentialEnvVars (exec_env.go), called from the
// sidecar-enabled branch of ensureSidecar (orchestrator_run.go ~L1093). It
// injects a credential's PlainValue into the agent env whenever the
// credential's EnvVarName is referenced by an MCP config (${VAR}) — matching
// on NAME ONLY, ignoring both the credential Type and keeperEnabled (the
// function has no keeper parameter at all).
//
// Consequence: a SECRET-typed credential whose EnvVarName is referenced in an
// agent/crew MCP server config is injected as cleartext into the agent's
// environment EVEN WITH KEEPER ON — readable via `env` / /proc/self/environ —
// directly contradicting the Keeper system prompt ("You do NOT have these
// credentials in your environment") and bypassing the /keeper/request audit
// gate the whole feature exists to enforce.
//
// This test PROVES the leak against current code (it passes = the hole is
// real). It is deliberately written to fail loudly if someone later closes the
// hole, at which point flip it to the companion expectation in
// TestInjectMCPCredentialEnvVars_SecretShouldBeGated_DESIRED below.
//
// NOT fixed here: gating SECRET in injectMCPCredentialEnvVars would 401 any MCP
// server that legitimately needs that secret (the MCP process can't call
// /keeper/request), so the correct remedy is a design decision (route MCP
// secrets through the sidecar/Keeper, or forbid SECRET-typed creds as MCP
// refs, or narrow the docs' absolute claim). Severity: HIGH for the stated
// Keeper contract, conditional on a SECRET being MCP-referenced.
func TestInjectMCPCredentialEnvVars_SecretLeaksUnderKeeper_CONFIRMED_HOLE(t *testing.T) {
	t.Parallel()
	// An HTTP MCP server whose Authorization header references a SECRET-typed
	// credential by name — a perfectly ordinary config.
	crewJSON := `{"mcpServers":{"internal":{"type":"http","url":"https://api.internal.example/sse","headers":{"Authorization":"Bearer ${PROD_DB_PASSWORD}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "s1", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2-cleartext"},
		},
	}

	// injectMCPCredentialEnvVars has no keeperEnabled parameter — there is no
	// way for the caller to ask it to withhold. It runs identically whether
	// Keeper is on or off.
	got := injectMCPCredentialEnvVars(req, nil)

	leaked := false
	for _, e := range got {
		if e == "PROD_DB_PASSWORD=hunter2-cleartext" {
			leaked = true
		}
	}
	if !leaked {
		t.Fatalf("expected the SECRET plaintext to be present in env (documenting the known leak); "+
			"if this now FAILS, the hole may be fixed — update this test and un-skip the DESIRED companion. env=%v", got)
	}
	t.Logf("CONFIRMED: SECRET PROD_DB_PASSWORD injected into agent env via MCP ref, bypassing Keeper. env=%v", got)
}

// TestInjectMCPCredentialEnvVars_SecretShouldBeGated_DESIRED encodes the
// behaviour the Keeper contract actually promises: with Keeper ON, a
// SECRET-typed credential must NOT be injected into the agent env even when an
// MCP config references it. It is skipped because the current code cannot
// satisfy it (injectMCPCredentialEnvVars takes no keeper flag). Un-skip and
// wire a keeper parameter through when the design decision above is made.
func TestInjectMCPCredentialEnvVars_SecretShouldBeGated_DESIRED(t *testing.T) {
	t.Skip("DESIRED behaviour, not yet implemented: injectMCPCredentialEnvVars has no keeper gate; " +
		"see TestInjectMCPCredentialEnvVars_SecretLeaksUnderKeeper_CONFIRMED_HOLE. Requires a design decision.")

	// Sketch of the assertion once a keeper-aware variant exists:
	crewJSON := `{"mcpServers":{"internal":{"type":"http","url":"https://api.internal.example/sse","headers":{"Authorization":"Bearer ${PROD_DB_PASSWORD}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "s1", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2-cleartext"},
		},
	}
	got := injectMCPCredentialEnvVars(req, nil) // would need: (req, nil, keeperEnabled=true)
	for _, e := range got {
		if strings.HasPrefix(e, "PROD_DB_PASSWORD=") {
			t.Fatalf("Keeper ON: SECRET must not be injected via MCP ref, got %q", e)
		}
	}
}

// TestInjectMCPCredentialEnvVars_NonSecretUnaffected documents that the
// finding is scoped to SECRET-vs-Keeper semantics: CLI_TOKEN and other
// env-legitimate types are SUPPOSED to be injected via MCP refs and that is
// correct behaviour (they carry no withhold claim). This guards against an
// over-broad future fix that would break MCP auth for the ungated types.
func TestInjectMCPCredentialEnvVars_NonSecretUnaffected(t *testing.T) {
	t.Parallel()
	crewJSON := `{"mcpServers":{"gh":{"type":"http","url":"https://mcp.gh.example/sse","headers":{"Authorization":"Bearer ${GH_TOKEN}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "c1", Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp_real"},
		},
	}
	got := injectMCPCredentialEnvVars(req, nil)
	found := false
	for _, e := range got {
		if e == "GH_TOKEN=ghp_real" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CLI_TOKEN referenced by MCP must be injected (correct behaviour); env=%v", got)
	}
}
