package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// credExecFake is the test double for ContainerProvider used by the
// writeCredentialFiles regression tests below. It records every Exec
// call (so the test can assert which User was passed and what
// script body actually got executed), and returns a configurable
// exit code from ExecInspect so the test can simulate the production
// permission-denied scenario without needing a real container.
type credExecFake struct {
	mu sync.Mutex

	// inputs the test inspects
	lastCfg    provider.ExecConfig
	execCalls  int
	scriptSeen string

	// outputs the test controls
	execErr        error
	inspectErr     error
	inspectExit    int
	inspectRunning bool
}

func (f *credExecFake) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls++
	f.lastCfg = cfg
	if len(cfg.Cmd) >= 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c" {
		f.scriptSeen = cfg.Cmd[2]
	}
	if f.execErr != nil {
		return nil, f.execErr
	}
	return &provider.ExecResult{
		ExecID: "exec-stub-1",
		Reader: io.NopCloser(strings.NewReader("")),
	}, nil
}

func (f *credExecFake) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return f.inspectRunning, f.inspectExit, f.inspectErr
}

// Unused-but-required ContainerProvider surface area. Each returns the
// zero value of its signature so it's obvious when a test inadvertently
// reaches a method these regressions don't cover.
func (f *credExecFake) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (f *credExecFake) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (f *credExecFake) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *credExecFake) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *credExecFake) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *credExecFake) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (f *credExecFake) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*credExecFake)(nil)

func quietCredLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// TestWriteCredentialFiles_RunsAsAgentUID pins the post-PR fix:
// previously the script ran as User="0:0" and silently failed on
// CapDrop:ALL containers; the only correct UID is the secrets dir
// owner (1001:1001). A test that asserts the literal User string is
// the simplest backstop against an accidental revert.
func TestWriteCredentialFiles_RunsAsAgentUID(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger()); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}
	if fake.lastCfg.User != "1001:1001" {
		t.Errorf("script must run as 1001:1001 (owner of secretsAgentDir), got %q", fake.lastCfg.User)
	}
}

// TestWriteCredentialFiles_ScriptOmitsChown locks down the matched
// half of the fix: dropping chown is what makes 1001-as-user safe
// (chown of non-root files by their owner to a different UID needs
// CAP_CHOWN, which the container doesn't have). If a future change
// adds a chown back, this test catches it before it makes "credential
// files written" lie again at runtime.
func TestWriteCredentialFiles_ScriptOmitsChown(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
		{ID: "c2", EnvVarName: "GITHUB_SSH", PlainValue: "-----BEGIN OPENSSH-----\n...\n", Type: "SSH_KEY"},
		{ID: "c3", EnvVarName: "VAULT_USER", PlainValue: "pw", Type: "USERPASS", Username: "alice"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger()); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}
	if strings.Contains(fake.scriptSeen, "chown") {
		t.Errorf("script must not call chown — UID 1001 already owns the files; got script:\n%s", fake.scriptSeen)
	}
}

// TestWriteCredentialFiles_ScriptRemovesPathBeforeWrite locks down
// the TOCTOU defence: each `echo … > path` redirect must be preceded
// by an `rm -f path` so a symlink planted by the previous agent
// session (warm container restart, same UID 1001) can't redirect
// the write to a 1001-writable target like /crew/shared/.memory/X
// or /output/<other-agent>/Y.
//
// The script is one `sh -c` string with `&&` between steps, so
// "preceded" means the rm-f literal appears at an earlier substring
// index than the matching `> path` literal. We assert both:
//
//  1. an `rm -f /secrets/agent-a/<envvar>` clause exists for every
//     credential file path the writer plans to create
//  2. that rm-f clause appears in the script before the `>`-redirect
//     that follows-or-creates that same path
//
// If either invariant breaks, a regression on the symlink TOCTOU is
// silently re-introduced — the writer behaves the same on a clean
// dir but corrupts attacker-chosen files on a warm restart. Locking
// the script structure here keeps the security property visible.
func TestWriteCredentialFiles_ScriptRemovesPathBeforeWrite(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
		{ID: "c2", EnvVarName: "GITHUB_SSH", PlainValue: "key", Type: "SSH_KEY"},
		{ID: "c3", EnvVarName: "VAULT_USER", PlainValue: "pw", Type: "USERPASS", Username: "alice"},
	}
	if err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger()); err != nil {
		t.Fatalf("writeCredentialFiles: %v", err)
	}

	// Every path the writer plans to create. Order is intentional —
	// matches the dispatch order in buildCredFileScript so the
	// before-relation we assert below is well defined.
	expectedPaths := []string{
		"/secrets/agent-a/GH_TOKEN",
		"/secrets/agent-a/ssh/GITHUB_SSH",
		"/secrets/agent-a/VAULT_USER_USERNAME",
		"/secrets/agent-a/VAULT_USER_PASSWORD",
		"/secrets/agent-a/.env",
	}
	for _, p := range expectedPaths {
		rmTok := "rm -f " + p
		writeTok := "> " + p
		rmIdx := strings.Index(fake.scriptSeen, rmTok)
		writeIdx := strings.Index(fake.scriptSeen, writeTok)
		if rmIdx < 0 {
			t.Errorf("missing TOCTOU guard for %q — script must contain %q before the redirect; got script:\n%s",
				p, rmTok, fake.scriptSeen)
			continue
		}
		if writeIdx < 0 {
			t.Errorf("write to %q missing from script entirely; got:\n%s", p, fake.scriptSeen)
			continue
		}
		if rmIdx > writeIdx {
			t.Errorf("rm-f for %q happens AFTER the redirect (rmIdx=%d, writeIdx=%d) — symlink window stays open. Script:\n%s",
				p, rmIdx, writeIdx, fake.scriptSeen)
		}
	}
}

// TestWriteCredentialFiles_NonZeroExitErrors is the load-bearing one.
// Pre-fix the orchestrator treated `Exec returned no Go error` as
// "the script succeeded" — which masked the permission-denied
// failures that this PR exists to surface. The check belongs on
// the writeCredentialFiles boundary because it's the place where
// "I tried to write N files" becomes a yes/no for the caller's
// warn-and-continue path in orchestrator_run.go.
func TestWriteCredentialFiles_NonZeroExitErrors(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{inspectExit: 1}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
	}
	err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger())
	if err == nil {
		t.Fatal("non-zero exit code from cred-write script must surface as error")
	}
	if !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("error should mention the exit code; got %v", err)
	}
}

func TestWriteCredentialFiles_InspectErrorPropagates(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{inspectErr: errors.New("daemon unreachable")}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
	}
	err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger())
	if err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Errorf("ExecInspect error must surface; got %v", err)
	}
}

// TestWriteCredentialFiles_StillRunningErrors catches the race where
// the docker daemon claims the exec is still active after we've
// drained its stdout to EOF. In practice docker doesn't do this, but
// returning the (running=true, exit=0) tuple as success would be a
// silent-correctness regression — explicitly fail instead.
func TestWriteCredentialFiles_StillRunningErrors(t *testing.T) {
	t.Parallel()
	fake := &credExecFake{inspectRunning: true}
	creds := []Credential{
		{ID: "c1", EnvVarName: "GH_TOKEN", PlainValue: "ghp_abc", Type: "CLI_TOKEN"},
	}
	err := writeCredentialFiles(context.Background(), fake, "ctr-x", "agent-a", creds,
		"/secrets/agent-a", "/secrets/shared", quietCredLogger())
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Errorf("running=true after EOF must error; got %v", err)
	}
}
