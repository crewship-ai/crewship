package orchestrator

// #1652: one credential that is not file-delivered must not cost the agent the
// credentials that are.
//
// The API mints a synthetic `_OAUTH_ACCESS_TOKEN:<credID>` entry for every
// OAuth MCP binding (internal/api/agent_config.go, resolveOAuthAccessTokens)
// and injectMCPOAuthTokens consumes it to write the MCP server's tokens.json.
// It is an OAUTH2 credential, i.e. Delivery=env in credpolicy, so
// buildCredFileScript has no business writing it to /secrets at all — but it
// used to validate the env var NAME before asking whether the credential was
// file-delivered, and `_OAUTH_ACCESS_TOKEN:<uuid>` is not a legal env var name.
// The name check returns a hard error, so the whole batch was abandoned and an
// agent that also held an SSH key or a CLI token started with nothing — or, on
// the fail-loud path a run carrying file-mounted credentials takes, did not
// start at all.

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

// TestBuildCredFileScript_SyntheticOAuthTokenDoesNotPoisonTheBatch is the unit
// half: the synthetic entry is skipped like any other env-delivered credential,
// and the file-mounted credentials sharing the batch with it are still written.
//
// The assertion is on the surviving FILES, not on `err == nil`. A fix that
// skipped everything — or that returned early on the first non-file credential
// — would satisfy a nil-error check while delivering exactly as little as the
// bug did.
func TestBuildCredFileScript_SyntheticOAuthTokenDoesNotPoisonTheBatch(t *testing.T) {
	t.Parallel()

	sshKey := pemFixture("OPENSSH PRIVATE KEY", "deploy-key-body")
	// A real credential id, assembled at runtime so the contiguous uuid never
	// appears in source (gitleaks reads it as a generic API key).
	credID := strings.Join([]string{"0f8c1b2e", "4d5a", "4f6b", "9c7d", "1e2f3a4b5c6d"}, "-")
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_xxx", Type: "CLI_TOKEN"},
		// The synthetic entry, exactly as resolveOAuthAccessTokens mints it:
		// a routing tag in the env-var slot, not a name anything can export.
		{ID: credID, EnvVarName: "_OAUTH_ACCESS_TOKEN:" + credID,
			PlainValue: "lin_access_token", Type: "OAUTH2"},
		{ID: "c2", EnvVarName: "DEPLOY_KEY", PlainValue: sshKey, Type: "SSH_KEY"},
	}

	script, count, _, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v — an OAuth MCP binding is not a file "+
			"credential; it must be skipped, not fail the batch", err)
	}
	if count != 2 {
		t.Errorf("file count = %d, want 2 (the CLI token and the SSH key; the "+
			"synthetic OAuth token is env-delivered and writes no file)", count)
	}
	for _, sub := range []string{
		"chmod 0400 /secrets/agent-a/GH_TOKEN",
		"chmod 0600 /secrets/agent-a/ssh/DEPLOY_KEY",
	} {
		if !strings.Contains(script, sub) {
			t.Errorf("script missing %q — the surviving credentials were dropped, "+
				"not merely un-poisoned\n%s", sub, script)
		}
	}
	if strings.Contains(script, "_OAUTH_ACCESS_TOKEN") {
		t.Errorf("script writes the synthetic OAuth entry to disk; it is "+
			"Delivery=env and belongs in tokens.json, not /secrets\n%s", script)
	}
	if strings.Contains(script, base64.StdEncoding.EncodeToString([]byte("lin_access_token"))) {
		t.Errorf("the OAuth access token's cleartext reached the credential-file script")
	}

	env := decodeEnvFromScript(t, script)
	for _, line := range []string{
		"GH_TOKEN=/secrets/agent-a/GH_TOKEN",
		"DEPLOY_KEY_PATH=/secrets/agent-a/ssh/DEPLOY_KEY",
	} {
		if !strings.Contains(env, line) {
			t.Errorf(".env missing %q\n%s", line, env)
		}
	}
}

// TestPreparePreflightDirs_OAuthMCPBindingDoesNotAbortAFileCredRun is the half
// that says the run starts. `fileCreds` is true here — the request carries an
// SSH key — and that is precisely the branch that treats a credential-write
// failure as fatal: it calls failRun and returns an error, so the agent CLI is
// never exec'd. An OAuth MCP binding must not be able to reach that branch.
func TestPreparePreflightDirs_OAuthMCPBindingDoesNotAbortAFileCredRun(t *testing.T) {
	o, c, req := preflightFixture(t)

	sshKey := pemFixture("OPENSSH PRIVATE KEY", "deploy-key-body")
	cliToken := "ghp_supersecret_value"
	req.MCPServers = []MCPServerConfig{{
		ID: "m1", Name: "linear", Transport: "http", Endpoint: "https://mcp.linear.app",
		Env: map[string]string{"LINEAR_CLIENT_ID": "${LINEAR_CLIENT_ID}"},
	}}
	req.Credentials = []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: cliToken, Type: "CLI_TOKEN"},
		{ID: "o1", EnvVarName: "LINEAR_CLIENT_ID", PlainValue: "cid", Type: "OAUTH2"},
		{ID: "o1", EnvVarName: "_OAUTH_ACCESS_TOKEN:o1", PlainValue: "lin_access_token", Type: "OAUTH2"},
		{ID: "c2", EnvVarName: "DEPLOY_KEY", PlainValue: sshKey, Type: "SSH_KEY"},
	}

	// The fail-loud branch is gated on this being true; if the accounting ever
	// stops seeing a file credential here the test would pass for the wrong
	// reason.
	if !hasFileMountedCreds(req.Credentials, false) {
		t.Fatal("fixture no longer carries file-mounted credentials — the fatal " +
			"branch this test exercises would not be reachable")
	}

	if _, _, err := o.preparePreflightDirs(context.Background(), req, nil, true, false, "run-1"); err != nil {
		t.Fatalf("preflight aborted the run: %v — an agent with an OAuth MCP "+
			"binding and a file-mounted credential must still start", err)
	}

	var delivered strings.Builder
	for _, e := range c.snapshot() {
		delivered.WriteString(e.stdin)
		delivered.WriteString(strings.Join(e.cfg.Cmd, " "))
	}
	joined := delivered.String()
	for name, value := range map[string]string{"GH_TOKEN": cliToken, "DEPLOY_KEY": sshKey} {
		if !strings.Contains(joined, base64.StdEncoding.EncodeToString([]byte(value))) {
			t.Errorf("%s never reached the container — the run started but the "+
				"credential batch is still being abandoned", name)
		}
	}
	if strings.Contains(joined, "/secrets/scout/_OAUTH_ACCESS_TOKEN") {
		t.Error("the synthetic OAuth entry was written under /secrets; it is " +
			"env-delivered and only injectMCPOAuthTokens should place it")
	}
}
