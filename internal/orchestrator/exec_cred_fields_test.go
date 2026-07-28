package orchestrator

import (
	"strings"
	"testing"
)

// Delivering the PARTS of a multi-part credential into a container —
// PRD-CREDENTIALS-V2 §2.2, P4.
//
// The API tier decides the names (<SLOT>_<KEY>) and resolves collisions against
// the delivered set. What it CANNOT know is the set of variables this package
// adds at mount time — HOME, the proxy fence, the dummy provider keys. So the
// property under test here is the last-line one: a part never overwrites a name
// that is already in the env block, and it never becomes a credential in its own
// right (no OAuth-token selection, no USERPASS layout, no CredStore provider
// mapping keyed off a part).

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(env))
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			out[e[:i]] = e[i+1:]
		}
	}
	return out
}

// TestBuildEnvVars_FieldsAccompanyTheirCredential is the exec path without a
// sidecar: a part lands next to the value it belongs to, under the name the API
// tier derived.
func TestBuildEnvVars_FieldsAccompanyTheirCredential(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug: "ada",
		Credentials: []Credential{{
			ID: "c1", EnvVarName: "AWS", PlainValue: "primary", Type: "CLI_TOKEN",
			Fields: []CredentialField{
				{EnvVar: "AWS_ACCESS_KEY_ID", Value: "AKIA"},
				{EnvVar: "AWS_SECRET_ACCESS_KEY", Value: "wJalr", IsSecret: true},
			},
		}},
	}
	got := envMap(t, BuildEnvVars(req, nil))
	for k, want := range map[string]string{
		"AWS":                   "primary",
		"AWS_ACCESS_KEY_ID":     "AKIA",
		"AWS_SECRET_ACCESS_KEY": "wJalr",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

// TestBuildEnvVars_FieldNeverOverwritesAnExistingName is the last-line guard.
// The API tier cannot see HOME — it is added here, from the agent slug — so a
// part named HOME must lose to the one the runtime set. Overwriting it would
// point the agent's whole tool chain (config files, caches, ssh) at a directory
// the operator never chose.
func TestBuildEnvVars_FieldNeverOverwritesAnExistingName(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug: "ada",
		Credentials: []Credential{{
			ID: "c1", EnvVarName: "X", PlainValue: "v", Type: "CLI_TOKEN",
			Fields: []CredentialField{{EnvVar: "HOME", Value: "/tmp/attacker"}},
		}},
	}
	if got := envMap(t, BuildEnvVars(req, nil))["HOME"]; got != "/crew/agents/ada" {
		t.Errorf("HOME = %q, want the runtime's /crew/agents/ada", got)
	}
}

// TestBuildEnvVars_NoFieldsIsByteIdentical is the compatibility guarantee at
// this layer: every credential in every install today has no parts, so the env
// block for one must not change by a single entry.
func TestBuildEnvVars_NoFieldsIsByteIdentical(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug:   "ada",
		Credentials: []Credential{{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp", Type: "CLI_TOKEN"}},
	}
	want := []string{
		"HOME=/crew/agents/ada",
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=",
		"CREWSHIP_CREW_ID=",
		"CREWSHIP_CHAT_ID=",
		"CREWSHIP_CREW_SHARED=/crew/shared",
		"GH_TOKEN=ghp",
	}
	got := BuildEnvVars(req, nil)
	if len(got) != len(want) {
		t.Fatalf("env = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("env[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBuildEnvVars_FieldIsNotItsOwnOAuthToken guards the one rename this package
// performs. resolveEnvVar redirects an AI_CLI_TOKEN (or any sk-ant-oat value) to
// CLAUDE_CODE_OAUTH_TOKEN. A part is not a credential, so it must never be
// redirected there — that would replace the agent's real Anthropic session with
// an unrelated string and break every run of the crew.
func TestBuildEnvVars_FieldIsNotItsOwnOAuthToken(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug: "ada",
		Credentials: []Credential{{
			ID: "c1", EnvVarName: "SESSION", PlainValue: "sk-ant-oat-real", Type: "AI_CLI_TOKEN",
			Fields: []CredentialField{{EnvVar: "SESSION_BACKUP", Value: "sk-ant-oat-other"}},
		}},
	}
	got := envMap(t, BuildEnvVars(req, nil))
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat-real" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q, want the credential's own value", got["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if got["SESSION_BACKUP"] != "sk-ant-oat-other" {
		t.Errorf("SESSION_BACKUP = %q, want the part delivered under its own name", got["SESSION_BACKUP"])
	}
}

// TestBuildEnvVarsSidecar_NonSecretPartsAlwaysArriveSecretsFollowTheChannel is
// the sidecar rule.
//
// A non-secret part is an identifier — region, account id, host — cleartext at
// rest by design, and no delivery channel can carry it for us: the sidecar proxy
// injects a credential into an HTTP request, it cannot inject a region into the
// agent's environment. So non-secret parts always land in env.
//
// A SECRET part is credential material and follows its credential exactly. An
// API_KEY is isolated behind the reverse proxy and never reaches env, so neither
// does its secret part — otherwise the part would be the leak the isolation
// exists to prevent.
func TestBuildEnvVarsSidecar_NonSecretPartsAlwaysArriveSecretsFollowTheChannel(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug:  "ada",
		CLIAdapter: "CLAUDE_CODE",
		Credentials: []Credential{
			{
				ID: "proxied", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "sk-real", Type: "API_KEY",
				Fields: []CredentialField{
					{EnvVar: "ANTHROPIC_API_KEY_ORG", Value: "org-42"},
					{EnvVar: "ANTHROPIC_API_KEY_BACKUP", Value: "sk-backup", IsSecret: true},
				},
			},
			{
				ID: "cli", EnvVarName: "AWS", PlainValue: "primary", Type: "CLI_TOKEN",
				Fields: []CredentialField{
					{EnvVar: "AWS_REGION", Value: "eu-central-1"},
					{EnvVar: "AWS_SECRET_ACCESS_KEY", Value: "wJalr", IsSecret: true},
				},
			},
		},
	}
	got := envMap(t, BuildEnvVarsSidecar(req, false))

	if got["ANTHROPIC_API_KEY_ORG"] != "org-42" {
		t.Errorf("ANTHROPIC_API_KEY_ORG = %q, want the non-secret part delivered", got["ANTHROPIC_API_KEY_ORG"])
	}
	if _, leaked := got["ANTHROPIC_API_KEY_BACKUP"]; leaked {
		t.Error("a SECRET part of a proxy-isolated credential reached the agent env; its credential deliberately does not")
	}
	if got["ANTHROPIC_API_KEY"] != "sk-ant-dummy-crewship-sidecar" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want the dummy — the real key stays in the sidecar", got["ANTHROPIC_API_KEY"])
	}
	if got["AWS_REGION"] != "eu-central-1" || got["AWS_SECRET_ACCESS_KEY"] != "wJalr" {
		t.Errorf("CLI_TOKEN parts = %q/%q, want both delivered alongside their credential",
			got["AWS_REGION"], got["AWS_SECRET_ACCESS_KEY"])
	}
}

// TestBuildEnvVarsSidecar_KeeperWithholdsSecretPartsToo carries the #1364
// contract to the parts. Under Keeper the agent's system prompt states the
// credential is NOT in its environment; a secret part sitting in /proc/self/environ
// would make that statement false and route around the /keeper/request audit
// gate. The non-secret part stays, on the same footing as credentials.username,
// which Keeper has never withheld either.
func TestBuildEnvVarsSidecar_KeeperWithholdsSecretPartsToo(t *testing.T) {
	req := AgentRunRequest{
		AgentSlug:  "ada",
		CLIAdapter: "CLAUDE_CODE",
		Credentials: []Credential{{
			ID: "s1", EnvVarName: "DB", PlainValue: "pw", Type: "SECRET",
			Fields: []CredentialField{
				{EnvVar: "DB_HOST", Value: "db.internal"},
				{EnvVar: "DB_PASSPHRASE", Value: "hunter2", IsSecret: true},
			},
		}},
	}
	got := envMap(t, BuildEnvVarsSidecar(req, true))
	if _, leaked := got["DB_PASSPHRASE"]; leaked {
		t.Error("a SECRET part was injected under Keeper; the prompt promises the value is not in the environment")
	}
	if got["DB_HOST"] != "db.internal" {
		t.Errorf("DB_HOST = %q, want the non-secret identifier still delivered", got["DB_HOST"])
	}

	off := envMap(t, BuildEnvVarsSidecar(req, false))
	if off["DB_PASSPHRASE"] != "hunter2" {
		t.Errorf("with Keeper off, DB_PASSPHRASE = %q, want the part delivered like its credential", off["DB_PASSPHRASE"])
	}
}

// TestBuildCredFileScript_PartsBecomeTheirOwnFiles generalises the USERPASS
// precedent: one credential already expands into two files
// (<envvar>_USERNAME / <envvar>_PASSWORD), and a multi-part credential expands
// into one more file per part — flat layout, 0400, .env mapping name → path, so
// nothing sensitive rides in the env block itself.
//
// The parts use the flat layout regardless of the credential's type: the
// per-type layouts encode what the PRIMARY value is (an SSH key needs 0600 in
// ssh/, a cert needs a .pem), and a region or an account id is none of those.
func TestBuildCredFileScript_PartsBecomeTheirOwnFiles(t *testing.T) {
	creds := []Credential{{
		EnvVarName: "DEPLOY", PlainValue: pemFixture("OPENSSH PRIVATE KEY", "x"), Type: "SSH_KEY",
		Fields: []CredentialField{
			{EnvVar: "DEPLOY_HOST", Value: "git.example.com"},
			{EnvVar: "DEPLOY_PASSPHRASE", Value: "hunter2", IsSecret: true},
		},
	}}
	script, count, err := buildCredFileScript(creds, "/secrets/agent-a", false)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	if count != 3 {
		t.Errorf("file count = %d, want 3 (the key plus its two parts)", count)
	}
	for _, sub := range []string{
		"chmod 0600 /secrets/agent-a/ssh/DEPLOY",
		"chmod 0400 /secrets/agent-a/DEPLOY_HOST",
		"chmod 0400 /secrets/agent-a/DEPLOY_PASSPHRASE",
	} {
		if !strings.Contains(script, sub) {
			t.Errorf("script missing %q\n%s", sub, script)
		}
	}
	env := decodeEnvFromScript(t, script)
	for _, line := range []string{
		"DEPLOY_PATH=/secrets/agent-a/ssh/DEPLOY",
		"DEPLOY_HOST=/secrets/agent-a/DEPLOY_HOST",
		"DEPLOY_PASSPHRASE=/secrets/agent-a/DEPLOY_PASSPHRASE",
	} {
		if !strings.Contains(env, line) {
			t.Errorf(".env missing %q\n%s", line, env)
		}
	}
}

// TestBuildCredFileScript_PartsOfAWithheldCredentialAreWithheldToo: a
// Keeper-gated credential writes no file at all, and neither do its parts — the
// whole credential is reachable only through /keeper/request.
func TestBuildCredFileScript_PartsOfAWithheldCredentialAreWithheldToo(t *testing.T) {
	creds := []Credential{{
		EnvVarName: "DB", PlainValue: "pw", Type: "SECRET",
		Fields: []CredentialField{{EnvVar: "DB_HOST", Value: "db.internal"}},
	}}
	script, count, err := buildCredFileScript(creds, "/secrets/agent-a", true)
	if err != nil {
		t.Fatalf("buildCredFileScript: %v", err)
	}
	if script != "" || count != 0 {
		t.Errorf("script = %q (count=%d), want nothing written under Keeper", script, count)
	}
}

// TestBuildCredFileScript_MalformedPartNameIsRejected keeps the sanitiser
// applying to parts. It matters more here than for the primary: the error
// aborts the whole script, so an unsanitised part name that reached this
// function would leave the agent with no secrets at all rather than with one
// bad file.
func TestBuildCredFileScript_MalformedPartNameIsRejected(t *testing.T) {
	creds := []Credential{{
		EnvVarName: "GH_TOKEN", PlainValue: "ghp", Type: "CLI_TOKEN",
		Fields: []CredentialField{{EnvVar: "GH_TOKEN;rm -rf /", Value: "x"}},
	}}
	if _, _, err := buildCredFileScript(creds, "/secrets/agent-a", false); err == nil {
		t.Fatal("expected an error for a part whose env var name fails the sanitiser")
	}
}
