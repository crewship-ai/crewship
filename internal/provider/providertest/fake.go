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
	// containerPIDs maps a synthetic CONTAINER-namespace pid back to the
	// exec id it belongs to — the only pid table runCommand's "kill" and
	// process-group-kill cases (and TmuxListPanePIDsCmd) resolve against,
	// modelling what a `kill`/`tmux list-panes` exec run INSIDE the
	// container can actually see. Deliberately a SEPARATE table from
	// ExecPID's reported pid (fakeExec.hostPID) — see fakeExec's doc for
	// why: that mismatch is #2365 itself, modelled so a hard-stop path that
	// still signals by ExecPID's pid fails here exactly like it does
	// against a real docker daemon.
	containerPIDs map[int]string
	// sessions maps a tmux session name (see HoldSessionCmd) to the exec id
	// running "under" it, so runCommand's "tmux kill-session"/"tmux
	// list-panes" cases can resolve a session name to its exec exactly like
	// a real per-container tmux server would.
	sessions map[string]string

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
	// hostPID is what ExecPID reports for this exec — modelled on dockerd's
	// ExecInspect Pid field, which is the exec's pid in the HOST pid
	// namespace (#2365). Deliberately drawn from a range disjoint from
	// containerPID below and NEVER resolvable by runCommand's "kill" or
	// "tmux" cases (both run as execs INSIDE the container): that mismatch
	// IS the bug B7b fixed, modelled here so a hard-stop path that still
	// signals by ExecPID's pid goes red exactly like it does against a real
	// docker daemon, while one that signals by tmux session name (or a
	// containerPID obtained via TmuxListPanePIDsCmd) does not.
	hostPID int
	// containerPID is a synthetic pid in the CONTAINER's own pid namespace
	// — what a real `tmux list-panes -F '#{pane_pid}'` run inside the
	// container would report. The only pid runCommand's "kill" and
	// process-group-kill cases resolve.
	containerPID int
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
	// ever stops when signalled — by containerPID via provider.KillSignalCmd
	// / provider.KillProcessGroupCmd, by tmux session via HoldSessionCmd
	// below, or via the test's global Unblock as a cleanup safety net — the
	// fake's stand-in for an agent CLI running inside a crew-shared
	// container. Exported so a hard-stop test (internal/api) can start one
	// exec per simulated agent and drive the real hard-stop code path
	// against it.
	HoldCmd = []string{"hold"}
)

// HoldSessionCmd is HoldCmd registered under a tmux session name — the
// fake's stand-in for setupTmuxExec's real tmux-wrapped exec
// (internal/orchestrator/orchestrator_exec_env.go), so a test can drive
// Tier 2 hard termination's actual signal, provider.TmuxKillSessionCmd(name)
// / provider.TmuxListPanePIDsCmd(name), rather than a bare pid. name is
// ordinarily orchestrator.TmuxSessionName(agentSlug).
func HoldSessionCmd(session string) []string { return []string{"hold", session} }

func fakeExitCmd(code int) []string { return []string{"exit", strconv.Itoa(code)} }

// NewFakeProvider returns a conforming in-memory provider.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		ContainerUserValue: DefaultSafeUser,
		StderrText:         "contract-stderr-marker",
		execs:              make(map[string]*fakeExec),
		containerPIDs:      make(map[int]string),
		sessions:           make(map[string]string),
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
	// Two disjoint ranges on purpose — see fakeExec's doc: hostPID (what
	// ExecPID reports) must never collide with a containerPID (what
	// runCommand's "kill"/"tmux" cases can resolve), the same way a real
	// exec's host-namespace pid and its container-namespace pid never do.
	containerPID := f.seq
	hostPID := f.seq + fakeHostPIDOffset
	entry := &fakeExec{done: make(chan struct{}), hostPID: hostPID, containerPID: containerPID, killCh: make(chan string, 2)}
	f.execs[execID] = entry
	f.containerPIDs[containerPID] = execID
	if len(cfg.Cmd) == 2 && cfg.Cmd[0] == "hold" && cfg.Cmd[1] != "" {
		f.sessions[cfg.Cmd[1]] = execID
	}
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
		// provider.KillSignalCmd's argv: one containerPID. Resolved ONLY
		// against containerPIDs — a caller that hands this ExecPID's
		// hostPID (the pre-#2365 bug) always misses here, exactly like it
		// misses against a real container's own pid table.
		pid, _ := strconv.Atoi(cfg.Cmd[2])
		sig := strings.TrimPrefix(cfg.Cmd[1], "-")
		code = f.signalContainerPID(pid, sig)
	case len(cfg.Cmd) >= 4 && cfg.Cmd[0] == "kill" && cfg.Cmd[2] == "--":
		// provider.KillProcessGroupCmd's argv: Tier 2's escalation, one exec
		// signalling every listed pane's whole group. Success if ANY listed
		// pid resolved — mirrors POSIX kill(1), which reports failure only
		// when every operand failed.
		sig := strings.TrimPrefix(cfg.Cmd[1], "-")
		code = 1
		for _, tok := range cfg.Cmd[3:] {
			pid, _ := strconv.Atoi(strings.TrimPrefix(tok, "-"))
			if c := f.signalContainerPID(pid, sig); c == 0 {
				code = 0
			}
		}
	case len(cfg.Cmd) == 4 && cfg.Cmd[0] == "tmux" && cfg.Cmd[1] == "kill-session" && cfg.Cmd[2] == "-t":
		// provider.TmuxKillSessionCmd's argv: Tier 2's PRIMARY signal
		// (#2365) — resolved by session NAME, a container-visible identity,
		// never a pid of either namespace. Ending session must never reach
		// a different session in the same (fake) container: sessions is
		// looked up by its exact name only.
		session := cfg.Cmd[3]
		f.mu.Lock()
		execID, ok := f.sessions[session]
		var target *fakeExec
		if ok {
			target = f.execs[execID]
		}
		f.mu.Unlock()
		if !ok {
			code = 1 // tmux: session not found
		} else {
			code = f.signalExec(target, "TERM")
		}
	case len(cfg.Cmd) == 6 && cfg.Cmd[0] == "tmux" && cfg.Cmd[1] == "list-panes" && cfg.Cmd[2] == "-t":
		// provider.TmuxListPanePIDsCmd's argv: reports the session's
		// containerPID on stdout, exactly what a real `tmux list-panes`
		// would report from inside the container — never a host pid.
		session := cfg.Cmd[3]
		f.mu.Lock()
		execID, ok := f.sessions[session]
		var containerPID int
		if ok {
			if e := f.execs[execID]; e != nil {
				containerPID = e.containerPID
			}
		}
		f.mu.Unlock()
		if !ok || containerPID == 0 {
			code = 1
		} else {
			_, _ = io.WriteString(pw, strconv.Itoa(containerPID)+"\n")
		}
	}
	if f.Breaks.LoseExitCode {
		code = 0
	}
	entry.exitCode = code
	_ = pw.Close()
	close(entry.done)
}

// signalContainerPID resolves pid against the container-local pid table
// (containerPIDs) and delivers sig to its owning exec — exactly what a real
// in-container `kill` can do, and ALL it can do: a hostPID (ExecPID's
// namespace) is simply absent from this table, matching a real container's
// own pid table not containing it either.
func (f *FakeProvider) signalContainerPID(pid int, sig string) int {
	f.mu.Lock()
	execID, ok := f.containerPIDs[pid]
	var target *fakeExec
	if ok {
		target = f.execs[execID]
	}
	f.mu.Unlock()
	return f.signalExec(target, sig)
}

// signalExec delivers sig to target's killCh (or reports the matching
// failure exit code) — the shared tail of every signal-delivery command the
// fake understands: pid-based kill, process-group kill, and tmux
// kill-session all resolve to a *fakeExec one way or another and then call
// this. Never touches any exec but target — no sibling's channel is ever
// written, by construction (the caller decides which target, this method
// only ever signals THAT one).
func (f *FakeProvider) signalExec(target *fakeExec, sig string) int {
	if target == nil {
		return 1 // kill: (<pid>) - No such process / tmux: session not found
	}
	select {
	case <-target.done:
		return 1 // already exited — nothing left to signal
	default:
	}
	select {
	case target.killCh <- sig:
		return 0
	default:
		return 0 // already has a pending signal queued; still a "success"
	}
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

// fakeHostPIDOffset separates hostPID's numeric range from containerPID's —
// see fakeExec's doc. Large enough that no realistic containerPID sequence
// in a single test could ever collide with it.
const fakeHostPIDOffset = 100000

// ExecPID implements provider.ExecPIDProvider. pid == 0 (nothing to signal)
// once the exec's done channel has closed, matching the interface's
// contract and the real docker provider's behaviour (dockerd reports Pid 0
// for a finished exec). Reports hostPID, not containerPID — see fakeExec's
// doc: this is deliberately NOT the pid runCommand's "kill"/"tmux" cases can
// resolve, modelling #2365.
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
		return entry.hostPID, nil
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
