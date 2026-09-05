package providertest

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
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
	// pids maps a synthetic pid back to the exec id it belongs to, so
	// runCommand's "kill" case (and ExecPID's callers) can resolve one from the other
	// exactly like a real container's pid table would.
	pids map[int]string

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
	// pid is a synthetic, unique-within-the-provider process id, assigned at
	// creation. It stands in for the real OS pid a container runtime would
	// report via ExecInspect, so the fake can prove out the exact primitive
	// B7 hard termination depends on: resolve a pid from an exec id, then
	// signal that pid — never the whole container.
	pid int
	// killCh delivers a signal name ("TERM", "KILL") to a running "hold"
	// exec (see runCommand). Buffered so a TERM immediately followed by a
	// KILL (the hard-stop grace-period escalation) never blocks the sender.
	// Deliberately NOT selected on ctx.Done() anywhere in "hold"'s case: a
	// real docker exec keeps running when the caller's context is
	// cancelled (the PRD's whole reason Tier 1 cannot promise a kill), and
	// the fake would be lying about that gap if ctx cancellation alone
	// could stop it.
	killCh chan string
}

// Fake command vocabulary. A Runtime's command fields are provider-specific by
// design (a live runtime needs real shell commands), so these are just what the
// in-memory fake understands.
var (
	fakeEchoStdinCmd = []string{"echo-stdin"}
	fakeStderrCmd    = []string{"stderr"}
	fakeBlockCmd     = []string{"block"}
	// HoldCmd starts a "process" that ignores context cancellation and only
	// ever stops when signalled by pid via provider.KillSignalCmd (or the
	// test's global Unblock, as a cleanup safety net) — the fake's
	// stand-in for an agent CLI running inside a crew-shared container.
	// Exported so an hardTerminateExec test (internal/api) can start one
	// exec per simulated agent and drive the real hard-stop code path
	// against it.
	HoldCmd = []string{"hold"}
)

func fakeExitCmd(code int) []string { return []string{"exit", strconv.Itoa(code)} }

// NewFakeProvider returns a conforming in-memory provider.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		ContainerUserValue: DefaultSafeUser,
		StderrText:         "contract-stderr-marker",
		execs:              make(map[string]*fakeExec),
		pids:               make(map[int]string),
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
	pid := f.seq
	entry := &fakeExec{done: make(chan struct{}), pid: pid, killCh: make(chan string, 2)}
	f.execs[execID] = entry
	f.pids[pid] = execID
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
	case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "hold":
		// Deliberately no case <-ctx.Done(): see fakeExec.killCh's doc.
		select {
		case sig := <-entry.killCh:
			switch sig {
			case "KILL":
				code = 137 // 128 + SIGKILL(9), the real shell convention
			default:
				code = 143 // 128 + SIGTERM(15)
			}
		case <-f.unblockCh:
			code = 143 // test cleanup safety net; treated like a TERM
		case <-time.After(30 * time.Second):
		}
	case len(cfg.Cmd) == 3 && cfg.Cmd[0] == "kill":
		// The exact argv hardTerminateExec issues for real: resolve the pid
		// back to its owning exec and deliver the signal on ITS killCh only
		// — never anything container-wide, and never any other exec's
		// channel, which is what proves a sibling agent is unaffected.
		pid, _ := strconv.Atoi(cfg.Cmd[2])
		sig := strings.TrimPrefix(cfg.Cmd[1], "-")
		f.mu.Lock()
		targetID, ok := f.pids[pid]
		var target *fakeExec
		if ok {
			target = f.execs[targetID]
		}
		f.mu.Unlock()
		switch {
		case !ok || target == nil:
			code = 1 // kill: (<pid>) - No such process
		default:
			select {
			case <-target.done:
				code = 1 // already exited — nothing left to signal
			default:
				select {
				case target.killCh <- sig:
					code = 0
				default:
					code = 0 // already has a pending signal queued; still a "success"
				}
			}
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

// ExecPID implements provider.ExecPIDProvider. pid == 0 (nothing to signal)
// once the exec's done channel has closed, matching the interface's
// contract and the real docker provider's behaviour (dockerd reports Pid 0
// for a finished exec).
func (f *FakeProvider) ExecPID(_ context.Context, execID string) (int, error) {
	f.mu.Lock()
	entry, ok := f.execs[execID]
	f.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("exec %s not found", execID)
	}
	select {
	case <-entry.done:
		return 0, nil
	default:
		return entry.pid, nil
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
	_ provider.ExecPIDProvider         = (*FakeProvider)(nil)
)
