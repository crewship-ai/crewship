package providertest

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// FakeProvider is an in-memory provider.ContainerProvider that satisfies every
// contract in this package, plus a Breaks switchboard that violates them one at
// a time.
//
// It earns its place twice. It lets the suite run in `go test ./...` with no
// container runtime anywhere, which is what makes the contracts CI-enforceable
// rather than aspirational. And it is how the suite itself is tested: a check
// that never fails is indistinguishable from a check that always passes, so
// mutation_test.go flips each Break and asserts the matching contract goes red.
type FakeProvider struct {
	// ContainerUserValue is the container's configured run-as user, returned by
	// the empty-User resolution path.
	ContainerUserValue string
	// StderrText is what the "stderr" command writes.
	StderrText string
	// Breaks selectively violates contracts. Zero value = fully conforming.
	Breaks Breaks

	mu              sync.Mutex
	calls           int
	lastUser        string
	lastAttachStdin bool
	seq             int
	execs           map[string]*fakeExec

	unblockOnce sync.Once
	unblockCh   chan struct{}
}

// Breaks names one deliberate contract violation each. They exist only so the
// suite can be proven to have teeth; nothing in production sets them.
type Breaks struct {
	// DiscardStdin drops ExecConfig.Stdin on the floor — the #1779 bug.
	DiscardStdin bool
	// StdinNoEOF streams the bytes but never half-closes, so the reading
	// process never terminates. Modelled as the non-zero exit the harness
	// convention assigns to "stdin never reached EOF".
	StdinNoEOF bool
	// AlwaysAttachStdin attaches stdin even when ExecConfig.Stdin is nil.
	AlwaysAttachStdin bool
	// IgnorePrivilegedGuard runs whatever user it is handed — the #1158 bug.
	IgnorePrivilegedGuard bool
	// EmptyUserPassthrough leaves an empty User empty instead of resolving the
	// container's real user, letting the runtime pick the image default.
	EmptyUserPassthrough bool
	// IgnoreAllowPrivileged refuses root even when the audited opt-in is set.
	IgnoreAllowPrivileged bool
	// DropStderr keeps stderr out of the output stream.
	DropStderr bool
	// NotRunning reports running=false for a live process.
	NotRunning bool
	// RunningExitZero reports exit code 0 while the process is still running.
	RunningExitZero bool
	// LoseExitCode reports 0 for every finished process.
	LoseExitCode bool
	// UnknownExecOK answers (false, 0, nil) for an exec id it never issued.
	UnknownExecOK bool
	// NameIgnoresID keys the container name on the slug alone (audit C1).
	NameIgnoresID bool
	// NoExecID returns an ExecResult with an empty ExecID.
	NoExecID bool
}

type fakeExec struct {
	done     chan struct{}
	exitCode int
}

// Fake command vocabulary. A Runtime's command fields are provider-specific by
// design (a live runtime needs real shell commands), so these are just what the
// in-memory fake understands.
var (
	fakeEchoStdinCmd = []string{"echo-stdin"}
	fakeStderrCmd    = []string{"stderr"}
	fakeBlockCmd     = []string{"block"}
)

func fakeExitCmd(code int) []string { return []string{"exit", strconv.Itoa(code)} }

// NewFakeProvider returns a conforming in-memory provider.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		ContainerUserValue: DefaultSafeUser,
		StderrText:         "contract-stderr-marker",
		execs:              make(map[string]*fakeExec),
		unblockCh:          make(chan struct{}),
	}
}

// NewFakeRuntime wires a FakeProvider into a Runtime with every observation
// hook populated, so the whole table runs.
func NewFakeRuntime(t *testing.T, p *FakeProvider) Runtime {
	t.Helper()
	t.Cleanup(p.Unblock)
	return Runtime{
		Provider:      p,
		ContainerID:   "fake-container",
		SafeUser:      DefaultSafeUser,
		EchoStdinCmd:  fakeEchoStdinCmd,
		ExitCmd:       fakeExitCmd,
		StderrCmd:     fakeStderrCmd,
		StderrText:    p.StderrText,
		BlockCmd:      fakeBlockCmd,
		Unblock:       p.Unblock,
		AttachedStdin: p.AttachedStdin,
		ExecUser:      p.ExecUser,
		RuntimeCalls:  p.RuntimeCalls,
	}
}

// Unblock releases every process started with the block command. Safe to call
// repeatedly and when nothing is blocked.
func (f *FakeProvider) Unblock() { f.unblockOnce.Do(func() { close(f.unblockCh) }) }

// AttachedStdin reports whether the last exec attached stdin.
func (f *FakeProvider) AttachedStdin() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAttachStdin
}

// ExecUser reports the user the last exec ran as.
func (f *FakeProvider) ExecUser() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastUser
}

// RuntimeCalls counts how many execs actually reached the "runtime".
func (f *FakeProvider) RuntimeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// resolveUser mirrors the real providers' #1158 guard.
func (f *FakeProvider) resolveUser(user string, allowPrivileged bool) (string, error) {
	if user == "" && !f.Breaks.EmptyUserPassthrough {
		user = f.ContainerUserValue
	}
	if f.Breaks.IgnorePrivilegedGuard || (user == "" && f.Breaks.EmptyUserPassthrough) {
		return user, nil
	}
	if f.Breaks.IgnoreAllowPrivileged {
		allowPrivileged = false
	}
	if !allowPrivileged && provider.IsPrivilegedExecUser(user) {
		return "", fmt.Errorf("refusing to run as privileged user %q", user)
	}
	return user, nil
}

func (f *FakeProvider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	user, err := f.resolveUser(cfg.User, cfg.AllowPrivileged)
	if err != nil {
		// Before the counter is touched: a refusal must be observable as "the
		// runtime was never asked".
		return nil, err
	}

	attach := cfg.Stdin != nil || f.Breaks.AlwaysAttachStdin

	f.mu.Lock()
	f.calls++
	f.lastUser = user
	f.lastAttachStdin = attach
	f.seq++
	execID := fmt.Sprintf("fake-exec-%d", f.seq)
	entry := &fakeExec{done: make(chan struct{})}
	f.execs[execID] = entry
	f.mu.Unlock()

	pr, pw := io.Pipe()
	go f.runCommand(ctx, cfg, entry, pw)

	if f.Breaks.NoExecID {
		execID = ""
	}
	return &provider.ExecResult{ExecID: execID, Reader: pr}, nil
}

// runCommand is the fake's "process": it interprets the command vocabulary,
// writes output, records an exit code and then closes both the output stream
// and the done channel — in that order, so a caller that drains to EOF and then
// inspects always sees a finished exec.
func (f *FakeProvider) runCommand(ctx context.Context, cfg provider.ExecConfig, entry *fakeExec, pw *io.PipeWriter) {
	code := 0
	switch {
	case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "echo-stdin":
		switch {
		case f.Breaks.StdinNoEOF && cfg.Stdin != nil:
			// The process is still waiting for an EOF that never comes; the
			// harness convention reports that as a non-zero exit.
			code = 97
		case cfg.Stdin != nil && !f.Breaks.DiscardStdin:
			if b, err := io.ReadAll(cfg.Stdin); err == nil {
				_, _ = pw.Write(b)
			}
		}
	case len(cfg.Cmd) > 1 && cfg.Cmd[0] == "exit":
		code, _ = strconv.Atoi(cfg.Cmd[1])
	case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stderr":
		if !f.Breaks.DropStderr {
			_, _ = io.WriteString(pw, f.StderrText)
		}
	case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "block":
		select {
		case <-f.unblockCh:
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
	if f.Breaks.LoseExitCode {
		code = 0
	}
	entry.exitCode = code
	_ = pw.Close()
	close(entry.done)
}

func (f *FakeProvider) ExecInspect(_ context.Context, execID string) (bool, int, error) {
	f.mu.Lock()
	entry, ok := f.execs[execID]
	f.mu.Unlock()
	if !ok {
		if f.Breaks.UnknownExecOK {
			return false, 0, nil
		}
		return false, -1, fmt.Errorf("exec %s not found", execID)
	}
	select {
	case <-entry.done:
		return false, entry.exitCode, nil
	default:
		if f.Breaks.NotRunning {
			return false, 0, nil
		}
		if f.Breaks.RunningExitZero {
			return true, 0, nil
		}
		return true, -1, nil
	}
}

func (f *FakeProvider) ExecInteractive(ctx context.Context, cfg provider.InteractiveExecConfig) (*provider.InteractiveExecResult, error) {
	// No AllowPrivileged on the interactive path, by design.
	if _, err := f.resolveUser(cfg.User, false); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.calls++
	f.seq++
	execID := fmt.Sprintf("fake-exec-%d", f.seq)
	entry := &fakeExec{done: make(chan struct{})}
	f.execs[execID] = entry
	f.mu.Unlock()
	close(entry.done)
	return &provider.InteractiveExecResult{ExecID: execID, Conn: nopReadWriteCloser{}}, nil
}

func (f *FakeProvider) ExecResize(context.Context, string, uint16, uint16) error { return nil }

type nopReadWriteCloser struct{}

func (nopReadWriteCloser) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopReadWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopReadWriteCloser) Close() error                { return nil }

func (f *FakeProvider) CrewContainerName(id, slug string) string {
	if f.Breaks.NameIgnoresID {
		return "crewship-team-" + slug
	}
	return "crewship-team-" + slug + "-" + id
}

// --- lifecycle methods: not what this suite asserts, kept minimal ---

func (f *FakeProvider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "fake-container", nil
}
func (f *FakeProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (f *FakeProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (f *FakeProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: "fake-container", State: "running"}, nil
}
func (f *FakeProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return &provider.ContainerMetrics{Timestamp: time.Now()}, nil
}
func (f *FakeProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

var (
	_ provider.ContainerProvider       = (*FakeProvider)(nil)
	_ provider.InteractiveExecProvider = (*FakeProvider)(nil)
)
