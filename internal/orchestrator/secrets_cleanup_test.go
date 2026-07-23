package orchestrator

// A3 (secret lifecycle hardening): after an agent run completes, the run's
// materialized /secrets/<slug> files must be removed from the container.
// Every run rewrites them at setup (writeCredentialFiles), so nothing relies
// on their persistence between runs — leaving them around only widens the
// window in which a compromised container process can read credentials the
// agent no longer needs. Cleanup is refcounted per container+agent so a
// concurrent run of the same agent is never yanked out from under.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

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
		name   string
		creds  []Credential
		keeper bool
		want   bool
	}{
		{"nil", nil, false, false},
		{"sidecar-injected only", []Credential{
			{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "x"},
			{Type: "AI_CLI_TOKEN", EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN", PlainValue: "x"},
			{Type: "OAUTH2", EnvVarName: "GH_OAUTH", PlainValue: "x"},
		}, false, false},
		{"cli token lands on disk", []Credential{
			{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "x"},
		}, false, true},
		{"secret lands on disk when keeper off", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: "x"},
		}, false, true},
		{"secret withheld when keeper on (nothing to clean up)", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: "x"},
		}, true, false},
		{"cli token still lands with keeper on", []Credential{
			{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "x"},
		}, true, true},
		{"secret + cli token with keeper on: cli token keeps files alive", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: "x"},
			{Type: "CLI_TOKEN", EnvVarName: "GH_TOKEN", PlainValue: "x"},
		}, true, true},
		{"generic secret lands on disk", []Credential{
			{Type: "GENERIC_SECRET", EnvVarName: "TOK", PlainValue: "x"},
		}, false, true},
		{"generic secret lands on disk even with keeper on", []Credential{
			{Type: "GENERIC_SECRET", EnvVarName: "TOK", PlainValue: "x"},
		}, true, true},
		{"userpass lands on disk", []Credential{
			{Type: "USERPASS", EnvVarName: "DB", PlainValue: "x", Username: "u"},
		}, false, true},
		{"ssh key lands on disk", []Credential{
			{Type: "SSH_KEY", EnvVarName: "DEPLOY", PlainValue: "x"},
		}, false, true},
		{"certificate lands on disk", []Credential{
			{Type: "CERTIFICATE", EnvVarName: "CA", PlainValue: "x"},
		}, false, true},
		{"file type without value is skipped by the writer", []Credential{
			{Type: "SECRET", EnvVarName: "DB_PASS", PlainValue: ""},
		}, false, false},
		{"file type without env var is skipped by the writer", []Credential{
			{Type: "SECRET", EnvVarName: "", PlainValue: "x"},
		}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasFileMountedCreds(c.creds, c.keeper); got != c.want {
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

// TOCTOU guard #1: a run that retained between the last-holder decision and
// the cleanup exec must veto the rm — cleanupAgentSecrets re-checks the hold
// count under the per-key lock before touching the container.
func TestCleanupAgentSecrets_SkipsWhenRetainedAgain(t *testing.T) {
	execd := false
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		execd = true
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}

	// Run A finishes: retain → release says "last holder".
	o.retainAgentSecrets("ctr-1", "writer")
	if !o.releaseAgentSecrets("ctr-1", "writer") {
		t.Fatal("sole hold release must report last holder")
	}
	// Run B starts before A's cleanup exec fires.
	o.retainAgentSecrets("ctr-1", "writer")

	o.cleanupAgentSecrets("ctr-1", "writer")
	if execd {
		t.Fatal("cleanup must re-check holds and skip the rm when a new run retained meanwhile")
	}
	// B finishing later still cleans up normally.
	if !o.releaseAgentSecrets("ctr-1", "writer") {
		t.Fatal("B is now the sole holder")
	}
	o.cleanupAgentSecrets("ctr-1", "writer")
	if !execd {
		t.Fatal("cleanup with zero holds must exec the rm")
	}
}

// TOCTOU guard #2: the rm exec and a concurrent credential write are mutually
// exclusive via the per-key lock — a starting run can't write files mid-rm
// (they'd be deleted the instant they landed).
func TestCleanupAgentSecrets_SerializesWithCredentialWrite(t *testing.T) {
	execStarted := make(chan struct{})
	execRelease := make(chan struct{})
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		close(execStarted)
		<-execRelease
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}

	cleanupDone := make(chan struct{})
	go func() {
		o.cleanupAgentSecrets("ctr-1", "writer")
		close(cleanupDone)
	}()
	<-execStarted // rm is now in flight, holding the key lock

	// A starting run retains and then takes the write lock (the order
	// orchestrator_run.go uses). It must block until the rm finishes.
	o.retainAgentSecrets("ctr-1", "writer")
	writerLocked := make(chan struct{})
	go func() {
		lk := o.agentSecretsLock("ctr-1", "writer")
		lk.Lock()
		close(writerLocked)
		lk.Unlock()
	}()

	select {
	case <-writerLocked:
		t.Fatal("credential-write lock acquired while the cleanup rm was still in flight")
	case <-time.After(50 * time.Millisecond):
		// expected: writer is blocked
	}

	close(execRelease)
	select {
	case <-writerLocked:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never acquired the lock after cleanup finished")
	}
	<-cleanupDone
	o.releaseAgentSecrets("ctr-1", "writer")
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

// End-to-end through RunAgent: the hold must already exist when the
// credential-write script executes (retain BEFORE write, or a finishing
// concurrent run could rm the freshly-written files), and the post-run rm
// must fire after the agent exec.
func TestRunAgent_SecretsRetainBeforeWriteThenCleanup(t *testing.T) {
	var (
		mu                sync.Mutex
		o                 *Orchestrator
		holdAtWrite       = -1
		agentSeen         bool
		cleanupSeen       bool
		cleanupAfterAgent bool
	)
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			switch {
			case strings.Contains(joined, "base64 -d"):
				mu.Lock()
				holdAtWrite = o.secretsHoldCount("c1", "test-agent")
				mu.Unlock()
			case strings.Contains(joined, "rm -rf '/secrets/test-agent'"):
				mu.Lock()
				cleanupSeen = true
				cleanupAfterAgent = agentSeen
				mu.Unlock()
			case strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-test-agent"):
				mu.Lock()
				agentSeen = true
				mu.Unlock()
				return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader("hello\n"))}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: secretsTestReader()}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}
	o = New(mc, newMemState(), secretsTestLogger())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "test-agent",
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "test",
		TimeoutSecs: 30,
		Credentials: []Credential{{Type: "SECRET", EnvVarName: "GH_TOKEN", PlainValue: "tok"}},
	}, func(AgentEvent) {})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if holdAtWrite < 1 {
		t.Errorf("hold count at credential-write time = %d, want >= 1 (retain must precede the write)", holdAtWrite)
	}
	if !cleanupSeen {
		t.Error("post-run /secrets cleanup exec never happened")
	}
	if !cleanupAfterAgent {
		t.Error("cleanup fired before the agent exec")
	}
	if n := o.secretsHoldCount("c1", "test-agent"); n != 0 {
		t.Errorf("hold count after run = %d, want 0", n)
	}
}

// Fail loudly (Docker < 26 posture): when the run carries file-mounted
// credentials and the /secrets setup fails — the mkdir preflight or the
// credential-write script — the run must ABORT with an actionable error, not
// warn-and-continue into a session with zero credentials.
func TestRunAgent_SecretsSetupFailureAbortsWithFileCreds(t *testing.T) {
	fileCred := []Credential{{Type: "SECRET", EnvVarName: "GH_TOKEN", PlainValue: "tok"}}
	cases := []struct {
		name     string
		failWhen func(joined string, cmd []string) bool
		creds    []Credential
		wantErr  bool
	}{
		{
			name: "mkdir failure with file creds aborts",
			failWhen: func(joined string, cmd []string) bool {
				return len(cmd) > 0 && cmd[0] == "mkdir" && strings.Contains(joined, "/secrets/test-agent")
			},
			creds:   fileCred,
			wantErr: true,
		},
		{
			name: "cred-write failure with file creds aborts",
			failWhen: func(joined string, cmd []string) bool {
				return strings.Contains(joined, "base64 -d")
			},
			creds:   fileCred,
			wantErr: true,
		},
		{
			name: "mkdir failure without file creds stays best-effort",
			failWhen: func(joined string, cmd []string) bool {
				return len(cmd) > 0 && cmd[0] == "mkdir" && strings.Contains(joined, "/secrets/test-agent")
			},
			creds:   []Credential{{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "k"}},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mc := &mockContainer{
				execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
					joined := strings.Join(cfg.Cmd, " ")
					if c.failWhen(joined, cfg.Cmd) {
						return nil, errors.New("exec failed (simulated)")
					}
					if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-test-agent") {
						return &provider.ExecResult{ExecID: "exec-1", Reader: io.NopCloser(strings.NewReader("hello\n"))}, nil
					}
					return &provider.ExecResult{ExecID: "noop", Reader: secretsTestReader()}, nil
				},
				inspectResult: struct {
					running  bool
					exitCode int
				}{false, 0},
			}
			o := New(mc, newMemState(), secretsTestLogger())

			err := o.RunAgent(context.Background(), AgentRunRequest{
				AgentID:     "a1",
				AgentSlug:   "test-agent",
				ChatID:      "s1",
				ContainerID: "c1",
				CLIAdapter:  "CLAUDE_CODE",
				UserMessage: "test",
				TimeoutSecs: 30,
				Credentials: c.creds,
			}, func(AgentEvent) {})

			if c.wantErr {
				if err == nil {
					t.Fatal("expected the run to abort when /secrets setup failed with file-mounted credentials")
				}
				if !strings.Contains(err.Error(), "Docker") {
					t.Errorf("error should mention the Docker >= 26 /secrets tmpfs requirement, got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("run without file-mounted creds must tolerate the failure, got: %v", err)
			}
		})
	}
}
