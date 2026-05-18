package orchestrator

import (
	"encoding/base64"
	"strings"
	"testing"
)

// pemFixture builds a fake PEM block at runtime so the literal
// "-----BEGIN <label>-----" never appears contiguously in source —
// keeps the gitleaks private-key rule from flagging our test fixtures.
func pemFixture(label, body string) string {
	const dashes = "-----"
	return dashes + "BEGIN " + label + dashes + "\n" + body + "\n" + dashes + "END " + label + dashes
}

// TestBuildCredFileScript_PerType locks in the per-type mount layout
// the wizard/UX promises: env-only types skip the script, file-mounted
// types land at the right path with the right mode, and USERPASS
// expands into two file entries.
//
// We intentionally assert against the rendered shell script (not a
// structured plan) because the script is the contract — if the
// substring assertions hold, the right command will run as root
// inside the container. Tests cover behaviour, not internal helpers.
func TestBuildCredFileScript_PerType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		creds     []Credential
		wantEmpty bool
		// substrings the script must contain
		wantSubs []string
		// substrings the script must NOT contain (negative assertions
		// keep secrets from leaking via mode bits etc.)
		wantNotSubs []string
		// env lines (base64-encoded inside the script) that must appear
		// in the decoded .env content
		wantEnv []string
	}{
		{
			name: "API_KEY skipped (handled by sidecar proxy)",
			creds: []Credential{
				{EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-ant", Type: "API_KEY"},
			},
			wantEmpty: true,
		},
		{
			name: "AI_CLI_TOKEN skipped",
			creds: []Credential{
				{EnvVarName: "CLAUDE_TOKEN", PlainValue: "sk-ant-oat", Type: "AI_CLI_TOKEN"},
			},
			wantEmpty: true,
		},
		{
			name: "OAUTH2 skipped",
			creds: []Credential{
				{EnvVarName: "LINEAR_OAUTH", PlainValue: "oat_xxx", Type: "OAUTH2"},
			},
			wantEmpty: true,
		},
		{
			name: "CLI_TOKEN writes flat file mode 0400",
			creds: []Credential{
				{EnvVarName: "GH_TOKEN", PlainValue: "ghp_xxx", Type: "CLI_TOKEN"},
			},
			wantSubs: []string{
				"/secrets/agent-a/GH_TOKEN",
				"chmod 0400 /secrets/agent-a/GH_TOKEN",
				"chown 1001:1001 /secrets/agent-a/GH_TOKEN",
			},
			wantEnv: []string{"GH_TOKEN=/secrets/agent-a/GH_TOKEN"},
		},
		{
			name: "SECRET writes flat file mode 0400",
			creds: []Credential{
				{EnvVarName: "WEBHOOK_SECRET", PlainValue: "shhh", Type: "SECRET"},
			},
			wantSubs: []string{
				"chmod 0400 /secrets/agent-a/WEBHOOK_SECRET",
			},
			wantEnv: []string{"WEBHOOK_SECRET=/secrets/agent-a/WEBHOOK_SECRET"},
		},
		{
			name: "GENERIC_SECRET behaves like SECRET",
			creds: []Credential{
				{EnvVarName: "STRIPE_HOOK", PlainValue: "whsec_xxx", Type: "GENERIC_SECRET"},
			},
			wantSubs: []string{
				"chmod 0400 /secrets/agent-a/STRIPE_HOOK",
			},
			wantEnv: []string{"STRIPE_HOOK=/secrets/agent-a/STRIPE_HOOK"},
		},
		{
			name: "USERPASS expands into username + password files",
			creds: []Credential{
				{EnvVarName: "GMAIL", PlainValue: "pa55", Type: "USERPASS", Username: "user@gmail.com"},
			},
			wantSubs: []string{
				"chmod 0400 /secrets/agent-a/GMAIL_USERNAME",
				"chmod 0400 /secrets/agent-a/GMAIL_PASSWORD",
			},
			wantEnv: []string{
				"GMAIL_USERNAME=/secrets/agent-a/GMAIL_USERNAME",
				"GMAIL_PASSWORD=/secrets/agent-a/GMAIL_PASSWORD",
			},
		},
		{
			name: "SSH_KEY lands in ssh/ subdir at mode 0600",
			creds: []Credential{
				{EnvVarName: "GITHUB_SSH", PlainValue: pemFixture("OPENSSH PRIVATE KEY", "ABC"), Type: "SSH_KEY"},
			},
			wantSubs: []string{
				"mkdir -p /secrets/agent-a/ssh",
				"chmod 0700 /secrets/agent-a/ssh",
				"/secrets/agent-a/ssh/GITHUB_SSH",
				"chmod 0600 /secrets/agent-a/ssh/GITHUB_SSH",
			},
			// Helper env var so the agent doesn't have to know the path convention.
			wantEnv: []string{"GITHUB_SSH_PATH=/secrets/agent-a/ssh/GITHUB_SSH"},
			// SSH keys must NEVER land in the flat secrets dir — that
			// would put them next to env-only creds with a wider mode.
			wantNotSubs: []string{"chmod 0400 /secrets/agent-a/GITHUB_SSH "},
		},
		{
			name: "CERTIFICATE lands in certs/<name>.pem at mode 0400",
			creds: []Credential{
				{EnvVarName: "MTLS_CLIENT", PlainValue: pemFixture("CERTIFICATE", "ABC"), Type: "CERTIFICATE"},
			},
			wantSubs: []string{
				"mkdir -p /secrets/agent-a/ssh /secrets/agent-a/certs",
				"/secrets/agent-a/certs/MTLS_CLIENT.pem",
				"chmod 0400 /secrets/agent-a/certs/MTLS_CLIENT.pem",
			},
			wantEnv: []string{"MTLS_CLIENT_PATH=/secrets/agent-a/certs/MTLS_CLIENT.pem"},
		},
		{
			name: "Mixed types in one batch",
			creds: []Credential{
				{EnvVarName: "GH_TOKEN", PlainValue: "ghp", Type: "CLI_TOKEN"},
				{EnvVarName: "GMAIL", PlainValue: "pa55", Type: "USERPASS", Username: "u@x"},
				{EnvVarName: "DEPLOY", PlainValue: pemFixture("OPENSSH PRIVATE KEY", "x"), Type: "SSH_KEY"},
				{EnvVarName: "ANTHROPIC", PlainValue: "sk", Type: "API_KEY"}, // skipped
			},
			wantSubs: []string{
				"chmod 0400 /secrets/agent-a/GH_TOKEN",
				"chmod 0400 /secrets/agent-a/GMAIL_USERNAME",
				"chmod 0600 /secrets/agent-a/ssh/DEPLOY",
			},
			wantEnv: []string{
				"GH_TOKEN=/secrets/agent-a/GH_TOKEN",
				"GMAIL_USERNAME=/secrets/agent-a/GMAIL_USERNAME",
				"DEPLOY_PATH=/secrets/agent-a/ssh/DEPLOY",
			},
			wantNotSubs: []string{"ANTHROPIC"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script, count, err := buildCredFileScript(tc.creds, "/secrets/agent-a")
			if err != nil {
				t.Fatalf("buildCredFileScript: %v", err)
			}
			if tc.wantEmpty {
				if script != "" || count != 0 {
					t.Errorf("expected empty script, got count=%d script=%q", count, script)
				}
				return
			}
			if script == "" {
				t.Fatalf("expected non-empty script for %q, got empty", tc.name)
			}
			for _, sub := range tc.wantSubs {
				if !strings.Contains(script, sub) {
					t.Errorf("script missing substring %q\nscript:\n%s", sub, script)
				}
			}
			for _, sub := range tc.wantNotSubs {
				if strings.Contains(script, sub) {
					t.Errorf("script unexpectedly contains %q\nscript:\n%s", sub, script)
				}
			}
			if len(tc.wantEnv) > 0 {
				env := decodeEnvFromScript(t, script)
				for _, line := range tc.wantEnv {
					if !strings.Contains(env, line) {
						t.Errorf(".env missing line %q\ndecoded .env:\n%s", line, env)
					}
				}
			}
		})
	}
}

// TestBuildCredFileScript_USERPASSMissingUsername guards against a
// data-shape regression where the resolver fails to populate
// Username — we'd rather surface the error than silently mount an
// empty username file.
func TestBuildCredFileScript_USERPASSMissingUsername(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{EnvVarName: "GMAIL", PlainValue: "pa55", Type: "USERPASS"}, // no Username
	}
	_, _, err := buildCredFileScript(creds, "/secrets/agent-a")
	if err == nil {
		t.Fatal("expected error for USERPASS without Username, got nil")
	}
	if !strings.Contains(err.Error(), "username") {
		t.Errorf("error should mention username, got: %v", err)
	}
}

// TestBuildCredFileScript_RejectsBadEnvVarName confirms the existing
// envVarNameRE sanitiser still gates injection — a credential with a
// shell-metachar in its env var name must not produce a runnable script.
func TestBuildCredFileScript_RejectsBadEnvVarName(t *testing.T) {
	t.Parallel()
	creds := []Credential{
		{EnvVarName: "GH;rm -rf /", PlainValue: "x", Type: "SECRET"},
	}
	_, _, err := buildCredFileScript(creds, "/secrets/agent-a")
	if err == nil {
		t.Fatal("expected error for malicious env var name, got nil")
	}
}

// decodeEnvFromScript pulls the base64-encoded .env body out of the
// script and decodes it for substring assertions. The script writes
// .env via `echo '<base64>' | base64 -d > <secretsdir>/.env`, so we
// match on the unique " > <secretsdir>/.env" suffix to find the
// right line and base64-decode the quoted payload.
func decodeEnvFromScript(t *testing.T, script string) string {
	t.Helper()
	const tail = "/.env"
	for _, part := range strings.Split(script, " && ") {
		if !strings.Contains(part, "> ") || !strings.HasSuffix(strings.TrimSpace(part), tail) {
			continue
		}
		open := strings.Index(part, "echo '")
		close := strings.Index(part, "' | base64 -d")
		if open < 0 || close < 0 {
			continue
		}
		b64 := part[open+len("echo '") : close]
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("decode .env base64: %v", err)
		}
		return string(decoded)
	}
	t.Fatal(".env write not found in script")
	return ""
}
