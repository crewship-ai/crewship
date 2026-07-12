package orchestrator

// A3 (secret lifecycle hardening): after an agent run completes, the run's
// materialized /secrets/<slug> files must be removed from the container.
// Every run rewrites them at setup (writeCredentialFiles), so nothing relies
// on their persistence between runs — leaving them around only widens the
// window in which a compromised container process can read credentials the
// agent no longer needs. Cleanup is refcounted per container+agent so a
// concurrent run of the same agent is never yanked out from under.

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func secretsTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func secretsTestReader() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

func TestBuildSecretsCleanupScript(t *testing.T) {
	cases := []struct {
		name string
		slug string
		want string
	}{
		{"simple slug", "writer", "rm -rf '/secrets/writer'"},
		{"slug with digits and dashes", "agent-2", "rm -rf '/secrets/agent-2'"},
		{"empty slug refused", "", ""},
		{"path traversal refused", "../shared", ""},
		{"absolute path refused", "/etc", ""},
		{"uppercase refused", "Writer", ""},
		{"quote injection refused", "a'b", ""},
		{"whitespace refused", "a b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildSecretsCleanupScript(c.slug); got != c.want {
				t.Errorf("buildSecretsCleanupScript(%q) = %q, want %q", c.slug, got, c.want)
			}
		})
	}
}

func TestHasFileMountedCreds(t *testing.T) {
	cases := []struct {
		name  string
		creds []Credential
		want  bool
	}{
		{"nil", nil, false},
		{"sidecar-injected only", []Credential{
			{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "x"},
			{Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "x"},
			{Type: "OAUTH2", EnvVarName: "GH_OAUTH", PlainValue: "x"},
		}, false},
		{"cli token lands on disk", []Credential{
			{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "x"},
		}, true},
		{"secret lands on disk", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: "x"},
		}, true},
		{"generic secret lands on disk", []Credential{
			{Type: "GENERIC_SECRET", EnvVarName: "TOK", PlainValue: "x"},
		}, true},
		{"userpass lands on disk", []Credential{
			{Type: "USERPASS", EnvVarName: "DB", PlainValue: "x", Username: "u"},
		}, true},
		{"ssh key lands on disk", []Credential{
			{Type: "SSH_KEY", EnvVarName: "DEPLOY", PlainValue: "x"},
		}, true},
		{"certificate lands on disk", []Credential{
			{Type: "CERTIFICATE", EnvVarName: "CA", PlainValue: "x"},
		}, true},
		{"file type without value is skipped by the writer", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: ""},
		}, false},
		{"file type without env var is skipped by the writer", []Credential{
			{Type: "SECRET", EnvVarName: "", PlainValue: "x"},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasFileMountedCreds(c.creds); got != c.want {
				t.Errorf("hasFileMountedCreds = %v, want %v", got, c.want)
			}
		})
	}
}

// Two concurrent runs of the same agent share /secrets/<slug>; only the LAST
// finisher may remove the directory or it vanishes under the other run.
func TestSecretsHoldRefcount(t *testing.T) {
	o := &Orchestrator{}

	o.retainAgentSecrets("ctr-1", "writer")
	o.retainAgentSecrets("ctr-1", "writer")

	if o.releaseAgentSecrets("ctr-1", "writer") {
		t.Fatal("first release of two holds must NOT allow cleanup (concurrent run still live)")
	}
	if !o.releaseAgentSecrets("ctr-1", "writer") {
		t.Fatal("last release must allow cleanup")
	}

	// Independent keys: a different agent (or container) doesn't interfere.
	o.retainAgentSecrets("ctr-1", "writer")
	o.retainAgentSecrets("ctr-1", "editor")
	o.retainAgentSecrets("ctr-2", "writer")
	if o.releaseAgentSecrets("ctr-1", "writer") != true {
		t.Fatal("sole hold for ctr-1/writer must allow cleanup regardless of other keys")
	}
	if o.releaseAgentSecrets("ctr-1", "editor") != true {
		t.Fatal("sole hold for ctr-1/editor must allow cleanup")
	}
	if o.releaseAgentSecrets("ctr-2", "writer") != true {
		t.Fatal("sole hold for ctr-2/writer must allow cleanup")
	}
}

func TestCleanupAgentSecrets_ExecsAsAgentUID(t *testing.T) {
	var got *provider.ExecConfig
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		got = &cfg
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}

	o.cleanupAgentSecrets("ctr-9", "writer")

	if got == nil {
		t.Fatal("cleanup did not exec")
	}
	if got.ContainerID != "ctr-9" {
		t.Errorf("ContainerID = %q, want ctr-9", got.ContainerID)
	}
	if got.User != "1001:1001" {
		t.Errorf("User = %q, want 1001:1001 (dir is 0700 agent-owned; root has no CAP_DAC_OVERRIDE)", got.User)
	}
	if len(got.Cmd) != 3 || got.Cmd[0] != "sh" || got.Cmd[1] != "-c" ||
		!strings.Contains(got.Cmd[2], "rm -rf '/secrets/writer'") {
		t.Errorf("Cmd = %v, want sh -c rm -rf '/secrets/writer'", got.Cmd)
	}
}

func TestCleanupAgentSecrets_InvalidSlugOrNilContainer_NoExecNoPanic(t *testing.T) {
	execd := false
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		execd = true
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}
	o.cleanupAgentSecrets("ctr-9", "../etc")
	if execd {
		t.Fatal("invalid slug must never reach an exec (shell command surface)")
	}

	// nil container (tests / --no-docker) must be a no-op, not a panic.
	o2 := &Orchestrator{logger: secretsTestLogger()}
	o2.cleanupAgentSecrets("ctr-9", "writer")
}
