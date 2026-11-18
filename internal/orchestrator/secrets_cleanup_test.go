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

// secretsTestReader stands in for a container that ran what it was given. The
// merged preflight script ends by printing its completion marker and Flush
// requires it as proof the script was delivered (#1779); a reader that returns
// nothing would model a runtime silently dropping stdin, which is a different
// fixture than these tests want.
func secretsTestReader() io.ReadCloser {
	return io.NopCloser(strings.NewReader(preflightDoneMarker + "\n"))
}

func TestBuildSecretsCleanupScript(t *testing.T) {
	cases := []struct {
		name      string
		slug, run string
		want      string
	}{
		// E0: the target is the RUN's directory, never the agent's. Removing
		// /secrets/<slug> would delete a concurrently-live sibling run's
		// credentials — a cleanup doing the exact cross-run damage this
		// package exists to prevent.
		{"simple slug", "writer", "run-a", "rm -rf '/secrets/writer/run-a'"},
		{"slug with digits and dashes", "agent-2", "c0k3xj9q0001", "rm -rf '/secrets/agent-2/c0k3xj9q0001'"},
		{"empty slug refused", "", "run-a", ""},
		{"path traversal refused", "../shared", "run-a", ""},
		{"absolute path refused", "/etc", "run-a", ""},
		{"uppercase refused", "Writer", "run-a", ""},
		{"quote injection refused", "a'b", "run-a", ""},
		{"whitespace refused", "a b", "run-a", ""},
		// The run id lands in the same rm and gets the same scrutiny.
		{"empty run id refused", "writer", "", ""},
		{"run id traversal refused", "writer", "../..", ""},
		{"run id quote injection refused", "writer", "a'b", ""},
		{"run id separator refused", "writer", "a/b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildSecretsCleanupScript(c.slug, c.run); got != c.want {
				t.Errorf("buildSecretsCleanupScript(%q, %q) = %q, want %q", c.slug, c.run, got, c.want)
			}
		})
	}
}

func TestBuildRunHomeCleanupScript(t *testing.T) {
	cases := []struct {
		name      string
		slug, run string
		want      string
	}{
		{"simple", "writer", "run-a", "rm -rf '/crew/runs/writer/run-a'"},
		// The onboarding setup crew's slugs lead with an underscore. The
		// secrets cleanup's stricter charset refuses those, which would have
		// meant the one crew every new install runs never cleaned up a run
		// home — so this one uses validSlugRe instead.
		{"leading underscore accepted", "_crewship-setup-guide", "run-a",
			"rm -rf '/crew/runs/_crewship-setup-guide/run-a'"},
		{"empty slug refused", "", "run-a", ""},
		{"traversal refused", "../agents", "run-a", ""},
		{"quote injection refused", "a'b", "run-a", ""},
		{"empty run id refused", "writer", "", ""},
		{"run id traversal refused", "writer", "../..", ""},
		{"run id quote injection refused", "writer", "a'b", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildRunHomeCleanupScript(c.slug, c.run); got != c.want {
				t.Errorf("buildRunHomeCleanupScript(%q, %q) = %q, want %q", c.slug, c.run, got, c.want)
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

// The refcount mechanism itself: only the LAST release of a key may clean up.
// E0 gave the key a third component (the run), so a hold is now per-run — but
// the arithmetic it is asked to do is unchanged and still has to hold.
func TestSecretsHoldRefcount(t *testing.T) {
	o := &Orchestrator{}

	o.retainAgentSecrets("ctr-1", "writer", "run-a")
	o.retainAgentSecrets("ctr-1", "writer", "run-a")

	if o.releaseAgentSecrets("ctr-1", "writer", "run-a") {
		t.Fatal("first release of two holds must NOT allow cleanup (something still live)")
	}
	if !o.releaseAgentSecrets("ctr-1", "writer", "run-a") {
		t.Fatal("last release must allow cleanup")
	}

	// Independent keys: a different agent, container, OR RUN doesn't interfere.
	o.retainAgentSecrets("ctr-1", "writer", "run-a")
	o.retainAgentSecrets("ctr-1", "editor", "run-a")
	o.retainAgentSecrets("ctr-2", "writer", "run-a")
	if o.releaseAgentSecrets("ctr-1", "writer", "run-a") != true {
		t.Fatal("sole hold for ctr-1/writer/run-a must allow cleanup regardless of other keys")
	}
	if o.releaseAgentSecrets("ctr-1", "editor", "run-a") != true {
		t.Fatal("sole hold for ctr-1/editor/run-a must allow cleanup")
	}
	if o.releaseAgentSecrets("ctr-2", "writer", "run-a") != true {
		t.Fatal("sole hold for ctr-2/writer/run-a must allow cleanup")
	}
}

// The overlap case, which is what the refcount was built for and what E0
// changes the shape of. TWO RUNS of ONE agent in ONE container: each is the
// sole holder of its own directory, so each finisher cleans up its own and
// neither can reach the other's.
//
// Before E0 both runs shared /secrets/<slug>, and the refcount's job was to
// stop the first finisher deleting files the second was still using. It did
// that correctly — and could do nothing about the real damage, which was that
// the second run's credential write had already OVERWRITTEN the first's files
// in place. Per-run directories are what fix that; this test pins that the
// bookkeeping followed them.
func TestSecretsHoldRefcount_TwoRunsOfOneAgentAreIndependent(t *testing.T) {
	o := &Orchestrator{}

	o.retainAgentSecrets("ctr-1", "writer", "run-a")
	o.retainAgentSecrets("ctr-1", "writer", "run-b")

	// A finishing while B is still live: A is nonetheless the last holder of
	// its OWN directory and must be allowed to remove it.
	if !o.releaseAgentSecrets("ctr-1", "writer", "run-a") {
		t.Error("run A must be allowed to clean up its own /secrets dir while run B is live")
	}
	// And B's hold is untouched by that.
	if n := o.secretsHoldCount("ctr-1", "writer", "run-b"); n != 1 {
		t.Errorf("run B's hold count = %d after run A finished, want 1", n)
	}
	if !o.releaseAgentSecrets("ctr-1", "writer", "run-b") {
		t.Error("run B must be allowed to clean up its own dir once it finishes")
	}

	// The scripts they would run name different directories — the property
	// the whole change rests on.
	a := buildSecretsCleanupScript("writer", "run-a")
	b := buildSecretsCleanupScript("writer", "run-b")
	if a == b || a == "" || b == "" {
		t.Errorf("two runs of one agent must clean up different directories; got %q and %q", a, b)
	}
	if strings.Contains(b, "run-a") {
		t.Errorf("run B's cleanup names run A's directory: %q", b)
	}
}

func TestCleanupAgentSecrets_ExecsAsAgentUID(t *testing.T) {
	var got *provider.ExecConfig
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		got = &cfg
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}

	o.cleanupAgentSecrets("ctr-9", "writer", "run-a")

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
		!strings.Contains(got.Cmd[2], "rm -rf '/secrets/writer/run-a'") {
		t.Errorf("Cmd = %v, want sh -c rm -rf '/secrets/writer/run-a'", got.Cmd)
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
	o.retainAgentSecrets("ctr-1", "writer", "run-a")
	if !o.releaseAgentSecrets("ctr-1", "writer", "run-a") {
		t.Fatal("sole hold release must report last holder")
	}
	// Run B starts before A's cleanup exec fires.
	o.retainAgentSecrets("ctr-1", "writer", "run-a")

	o.cleanupAgentSecrets("ctr-1", "writer", "run-a")
	if execd {
		t.Fatal("cleanup must re-check holds and skip the rm when a new run retained meanwhile")
	}
	// B finishing later still cleans up normally.
	if !o.releaseAgentSecrets("ctr-1", "writer", "run-a") {
		t.Fatal("B is now the sole holder")
	}
	o.cleanupAgentSecrets("ctr-1", "writer", "run-a")
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
		o.cleanupAgentSecrets("ctr-1", "writer", "run-a")
		close(cleanupDone)
	}()
	<-execStarted // rm is now in flight, holding the key lock

	// A starting run retains and then takes the write lock (the order
	// orchestrator_run.go uses). It must block until the rm finishes.
	o.retainAgentSecrets("ctr-1", "writer", "run-a")
	writerLocked := make(chan struct{})
	go func() {
		lk := o.agentSecretsLock("ctr-1", "writer", "run-a")
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
	o.releaseAgentSecrets("ctr-1", "writer", "run-a")
}

func TestCleanupAgentSecrets_InvalidSlugOrNilContainer_NoExecNoPanic(t *testing.T) {
	execd := false
	ctr := &mockContainer{execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		execd = true
		return &provider.ExecResult{ExecID: "e1", Reader: secretsTestReader()}, nil
	}}
	o := &Orchestrator{container: ctr, logger: secretsTestLogger()}
	o.cleanupAgentSecrets("ctr-9", "../etc", "run-a")
	if execd {
		t.Fatal("invalid slug must never reach an exec (shell command surface)")
	}

	// nil container (tests / --no-docker) must be a no-op, not a panic.
	o2 := &Orchestrator{logger: secretsTestLogger()}
	o2.cleanupAgentSecrets("ctr-9", "writer", "run-a")
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
				holdAtWrite = o.secretsHoldCount("c1", "test-agent", "run-a")
				mu.Unlock()
			case strings.Contains(joined, "rm -rf '/secrets/test-agent/run-a'"):
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
		RunID:       "run-a",
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
	if n := o.secretsHoldCount("c1", "test-agent", "run-a"); n != 0 {
		t.Errorf("hold count after run = %d, want 0", n)
	}
}

// Fail loudly (Docker < 26 posture): when the run carries file-mounted
// credentials and the /secrets setup fails — the directory step or the
// credential-write step — the run must ABORT with an actionable error, not
// warn-and-continue into a session with zero credentials.
//
// Both steps ride the merged preflight script since #1646, so the failure
// arrives as the script's trailing "which step failed" line rather than as an
// error from that step's own exec. That the fail-loud posture still keys off a
// NAMED step is exactly the reporting property the merge had to preserve.
func TestRunAgent_SecretsSetupFailureAbortsWithFileCreds(t *testing.T) {
	fileCred := []Credential{{Type: "SECRET", EnvVarName: "GH_TOKEN", PlainValue: "tok"}}
	cases := []struct {
		name     string
		failStep string
		creds    []Credential
		wantErr  bool
	}{
		{
			name:     "agent-dirs step failure with file creds aborts",
			failStep: preflightStepAgentDirs,
			creds:    fileCred,
			wantErr:  true,
		},
		{
			name:     "cred-write step failure with file creds aborts",
			failStep: preflightStepCredentials,
			creds:    fileCred,
			wantErr:  true,
		},
		{
			name:     "agent-dirs step failure without file creds stays best-effort",
			failStep: preflightStepAgentDirs,
			creds:    []Credential{{Type: "API_KEY", EnvVarName: "ANTHROPIC_API_KEY", PlainValue: "k"}},
			wantErr:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mc := &mockContainer{
				execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
					joined := strings.Join(cfg.Cmd, " ")
					if stdin := covStdin(cfg); strings.Contains(stdin, preflightStepMarker+c.failStep) {
						return &provider.ExecResult{
							ExecID: "preflight-fail",
							Reader: io.NopCloser(strings.NewReader(preflightFailMarker + c.failStep + "\n")),
						}, nil
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
