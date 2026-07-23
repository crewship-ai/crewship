package orchestrator

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// ── SECURITY FINDING (#1362) — now CLOSED ────────────────────────────────────
//
// The "gate SECRET credential files on Keeper state" fix, and its docs
// (docs/guides/credentials.mdx), assert an absolute delivery contract:
//
//	"Keeper enabled — the SECRET is not written to the agent's filesystem at
//	 all, and it is not injected as an environment variable."
//
// The FILE path (buildCredFileScript) and the primary env path
// (BuildEnvVarsSidecar) both honour that. A THIRD delivery path used to NOT:
// injectMCPCredentialEnvVars (exec_env.go), called from the sidecar-enabled
// branch of ensureSidecar (orchestrator_run.go ~L1093). It injected a
// credential's PlainValue into the agent env whenever the credential's
// EnvVarName was referenced by an MCP config (${VAR}) — matching on NAME ONLY,
// ignoring both the credential Type and keeperEnabled.
//
// Consequence (pre-fix): a SECRET-typed credential whose EnvVarName was
// referenced in an agent/crew MCP server config was injected as cleartext into
// the agent's environment EVEN WITH KEEPER ON — bypassing the /keeper/request
// audit gate the whole feature exists to enforce.
//
// #1362 closes the hole: injectMCPCredentialEnvVars now takes keeperEnabled and
// withholds SECRET-typed credentials from the MCP env when Keeper is on. This
// test asserts the leak is CLOSED under Keeper, and that with Keeper OFF the
// SECRET is still injected (unchanged legacy behaviour — the MCP server needs
// the value and there is no Keeper to route it through).
func TestInjectMCPCredentialEnvVars_SecretWithheldUnderKeeper(t *testing.T) {
	t.Parallel()
	// An HTTP MCP server whose Authorization header references a SECRET-typed
	// credential by name — a perfectly ordinary config.
	crewJSON := `{"mcpServers":{"internal":{"type":"http","url":"https://api.internal.example/sse","headers":{"Authorization":"Bearer ${PROD_DB_PASSWORD}"}}}}`
	newReq := func() AgentRunRequest {
		return AgentRunRequest{
			CrewMCPConfigJSON: crewJSON,
			Credentials: []Credential{
				{ID: "s1", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2-cleartext"},
			},
		}
	}

	// Keeper ON: the SECRET must NOT be injected into the MCP env, and a warning
	// naming the credential must be emitted (without the value).
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	got := injectMCPCredentialEnvVars(newReq(), nil, true, logger)
	for _, e := range got {
		if strings.HasPrefix(e, "PROD_DB_PASSWORD=") {
			t.Fatalf("Keeper ON: SECRET must not be injected via MCP ref, got %q (env=%v)", e, got)
		}
	}
	logOut := buf.String()
	if !strings.Contains(logOut, "PROD_DB_PASSWORD") {
		t.Fatalf("Keeper ON: expected a warning naming the withheld SECRET, log=%q", logOut)
	}
	if strings.Contains(logOut, "hunter2-cleartext") {
		t.Fatalf("Keeper ON: warning must never contain the secret value, log=%q", logOut)
	}

	// Keeper OFF: legacy behaviour is unchanged — the SECRET IS injected so the
	// MCP server can authenticate.
	gotOff := injectMCPCredentialEnvVars(newReq(), nil, false, logger)
	leaked := false
	for _, e := range gotOff {
		if e == "PROD_DB_PASSWORD=hunter2-cleartext" {
			leaked = true
		}
	}
	if !leaked {
		t.Fatalf("Keeper OFF: SECRET must still be injected (legacy, unchanged); env=%v", gotOff)
	}
}

// TestInjectMCPCredentialEnvVars_SecretShouldBeGated_DESIRED is the primary
// assertion of the Keeper contract: with Keeper ON, a SECRET-typed credential
// must NOT be injected into the agent env even when an MCP config references it.
// Formerly skipped (the code could not satisfy it); #1362 implements the gate,
// so it now runs.
func TestInjectMCPCredentialEnvVars_SecretShouldBeGated_DESIRED(t *testing.T) {
	t.Parallel()
	crewJSON := `{"mcpServers":{"internal":{"type":"http","url":"https://api.internal.example/sse","headers":{"Authorization":"Bearer ${PROD_DB_PASSWORD}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "s1", Type: "SECRET", EnvVarName: "PROD_DB_PASSWORD", PlainValue: "hunter2-cleartext"},
		},
	}
	got := injectMCPCredentialEnvVars(req, nil, true, nil) // Keeper ON, nil logger tolerated
	for _, e := range got {
		if strings.HasPrefix(e, "PROD_DB_PASSWORD=") {
			t.Fatalf("Keeper ON: SECRET must not be injected via MCP ref, got %q", e)
		}
	}
}

// TestInjectMCPCredentialEnvVars_NonSecretUnaffected documents that the gate is
// scoped to SECRET-vs-Keeper semantics: CLI_TOKEN and other env-legitimate
// types are SUPPOSED to be injected via MCP refs and that stays correct even
// with Keeper ON (they carry no withhold claim). This guards against an
// over-broad fix that would break MCP auth for the ungated types.
func TestInjectMCPCredentialEnvVars_NonSecretUnaffected(t *testing.T) {
	t.Parallel()
	crewJSON := `{"mcpServers":{"gh":{"type":"http","url":"https://mcp.gh.example/sse","headers":{"Authorization":"Bearer ${GH_TOKEN}"}}}}`
	req := AgentRunRequest{
		CrewMCPConfigJSON: crewJSON,
		Credentials: []Credential{
			{ID: "c1", Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "ghp_real"},
		},
	}
	// Keeper ON — a non-SECRET type must STILL be injected.
	got := injectMCPCredentialEnvVars(req, nil, true, nil)
	found := false
	for _, e := range got {
		if e == "GH_TOKEN=ghp_real" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CLI_TOKEN referenced by MCP must be injected even under Keeper (correct behaviour); env=%v", got)
	}
}
