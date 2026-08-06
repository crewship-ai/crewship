// Package providertest holds the provider-agnostic contract suite that every
// provider.ContainerProvider implementation must pass.
//
// Why this package exists. provider.ContainerProvider constrains signatures
// only: an implementation satisfies it by COMPILING. Every semantic guarantee
// the callers actually depend on — that Exec delivers stdin, that it refuses to
// run as root, that ExecInspect reports a real exit code — lived in ONE
// provider's own test directory, so a second provider could (and did) ship with
// none of them. The apple provider's Exec silently discarded ExecConfig.Stdin,
// which made six orchestrator preflight steps execute nothing and report
// success (#1779); it also ignored ExecConfig.AllowPrivileged entirely, so the
// #1158 fail-closed guard simply did not exist on macOS. Both are contracts
// docker had pinned for months in its own package, where a new provider author
// would never look.
//
// The two files named *conformance* in the provider packages are independently
// hand-written, share zero code and assert container-CREATION semantics only —
// `grep Stdin` over both returns nothing. This package is the missing half: the
// EXEC/lifecycle contract, written once, executed against every provider.
//
// # Tiers
//
// Contracts split into two tiers, and the split is explicit rather than
// implied:
//
//   - The SEMANTIC tier (RunContractSuite) runs everywhere, in ordinary `go
//     test ./...`, with no container runtime installed. Each provider supplies
//     a Runtime backed by a fake of its own runtime — a fake Docker daemon over
//     httptest for docker, a fake `container` binary on PATH for apple. This is
//     where argument construction, guard behaviour and error classification are
//     pinned, and it is the tier that catches the two bugs above.
//
//   - The LIVE tier (RunLiveContractSuite) runs the same table against a real
//     runtime, and is gated because it needs one. It automatically drops the
//     contracts that are only observable by watching what the provider asked
//     the runtime to do (Contract.FakeOnly) — you cannot ask a real Docker
//     daemon "were you told to attach stdin?".
//
// A Runtime declares what it can observe by populating hook fields. A contract
// whose hooks are absent reports itself SKIPPED with a reason rather than
// silently passing — "not asserted" must never read as "asserted and fine",
// which is the exact failure mode this package was written to end.
package providertest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// DefaultSafeUser is the non-root user contracts run their commands as when a
// Runtime does not name one. It matches the uid:gid crew containers are created
// with on both shipping providers.
const DefaultSafeUser = "1001:1001"

// Runtime is one provider wired to a container runtime — fake or live — plus
// the few observations the ContainerProvider interface cannot express.
//
// Everything the contracts need to DO goes through Provider (the interface
// under test). Everything they need to SEE that the interface does not return
// is a hook; a nil hook means "this harness cannot observe that", and the
// contracts needing it skip with a reason.
type Runtime struct {
	// Provider is the implementation under test.
	Provider provider.ContainerProvider

	// ContainerID identifies a container the runtime knows about. Contracts
	// never create one: container creation is EnsureCrewRuntime's contract and
	// is already covered by each provider's own *conformance* tests.
	ContainerID string

	// SafeUser is a non-root user the runtime accepts. Empty = DefaultSafeUser.
	SafeUser string

	// EchoStdinCmd must copy the process's stdin to its stdout and exit 0 —
	// AND must exit non-zero if stdin never reaches EOF. That second half is
	// what makes the "write side is half-closed so the process observes EOF"
	// clause of ExecConfig.Stdin observable: a provider that streams the bytes
	// but never half-closes leaves a real `cat` hanging forever, which is a
	// silent hang in production and must be a red test here.
	EchoStdinCmd []string

	// ExitCmd returns a command that exits with the given code and prints
	// nothing. Used to prove ExecInspect reports the process's REAL status.
	ExitCmd func(code int) []string

	// StderrCmd writes StderrText to stderr and exits 0. ExecResult.Reader is
	// documented as "the output stream" (singular), so stderr must not be
	// dropped — a provider that demuxes only stdout loses every diagnostic a
	// failing preflight step prints.
	StderrCmd  []string
	StderrText string

	// BlockCmd runs until Unblock is called. Both must be set together; either
	// missing skips the ExecInspect-while-running contracts. Unblock must be
	// safe to call more than once and when nothing is blocked.
	BlockCmd []string
	Unblock  func()

	// AttachedStdin reports whether the provider asked the runtime to attach
	// stdin for the most recent exec. Fake runtimes only.
	AttachedStdin func() bool

	// ExecUser reports the user string the provider asked the runtime to run
	// the most recent exec as, or "" if it asked for none. Fake runtimes only.
	ExecUser func() string

	// RuntimeCalls counts how many times the provider has invoked the runtime
	// at all (any request/process). It is how "the refusal happened BEFORE the
	// command ran" is asserted — an error returned after the process already
	// started is not a fail-closed guard. Fake runtimes only.
	RuntimeCalls func() int
}

func (rt Runtime) safeUser() string {
	if rt.SafeUser != "" {
		return rt.SafeUser
	}
	return DefaultSafeUser
}

// Result is one contract's verdict. Contracts return a verdict rather than
// calling t.Errorf so the suite itself is testable: a mutation test can assert
// that breaking an implementation produces violations, which is the only way to
// know these checks have teeth.
type Result struct {
	// Skipped, when non-empty, says why the harness cannot express this
	// contract. Never a pass.
	Skipped string
	// Violations lists every way the implementation broke the contract.
	Violations []string
}

func (r *Result) violate(format string, args ...any) {
	r.Violations = append(r.Violations, fmt.Sprintf(format, args...))
}

func (r *Result) skip(format string, args ...any) Result {
	r.Skipped = fmt.Sprintf(format, args...)
	return *r
}

// Contract is one documented semantic of provider.ContainerProvider.
type Contract struct {
	// Name is the subtest name.
	Name string
	// Why cites the source of the guarantee — a doc comment, an issue, or the
	// incident that proved it matters. A contract nobody can trace back to a
	// requirement gets weakened the first time it is inconvenient.
	Why string
	// FakeOnly marks a contract that is only observable by watching what the
	// provider asked the runtime to do, so it cannot run against a live one.
	FakeOnly bool
	// Check returns the verdict. It must not call t.
	Check func(ctx context.Context, rt Runtime) Result
}

// RunContractSuite executes every contract against the harness. newRuntime is
// called once per subtest so a fake runtime's recorded state is never shared
// between contracts.
//
// Subtests are deliberately NOT parallel: the apple harness installs a fake CLI
// via t.Setenv, which panics under t.Parallel.
func RunContractSuite(t *testing.T, newRuntime func(t *testing.T) Runtime) {
	t.Helper()
	runSuite(t, newRuntime, false)
}

// RunLiveContractSuite executes the subset of contracts that are observable
// through the ContainerProvider interface alone against a real runtime. Gate it
// on the runtime actually being present — this is the tier that needs one.
func RunLiveContractSuite(t *testing.T, newRuntime func(t *testing.T) Runtime) {
	t.Helper()
	runSuite(t, newRuntime, true)
}

func runSuite(t *testing.T, newRuntime func(t *testing.T) Runtime, live bool) {
	t.Helper()
	for _, c := range Contracts() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			if live && c.FakeOnly {
				t.Skipf("fake-runtime-only contract: %s is asserted by inspecting what the provider asked the runtime to do", c.Name)
			}
			rt := newRuntime(t)
			// A generous ceiling: every contract here either completes in
			// milliseconds or is hanging, and a hang must fail rather than
			// wedge the package's test binary.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			res := c.Check(ctx, rt)
			if res.Skipped != "" {
				t.Skipf("harness cannot express this contract: %s", res.Skipped)
			}
			for _, v := range res.Violations {
				t.Errorf("CONTRACT VIOLATED: %s\n  %s\n  why: %s", c.Name, v, c.Why)
			}
		})
	}
}

// Contracts returns the table. Exported so a provider can enumerate what it is
// being held to (and so the mutation test can drive checks directly).
func Contracts() []Contract {
	return []Contract{
		contractStdinReachesProcess(),
		contractStdinHalfClosed(),
		contractNilStdinNotAttached(),
		contractRefusesPrivilegedUser(),
		contractEmptyUserResolvesNonRoot(),
		contractAllowPrivilegedOptIn(),
		contractExecResultShape(),
		contractStderrInOutput(),
		contractInspectReportsRunning(),
		contractInspectRunningNotSuccess(),
		contractInspectReportsExitCode(),
		contractInspectUnknownExecErrors(),
		contractCrewContainerNameKeyedByID(),
		contractInteractiveRefusesPrivileged(),
	}
}

// ---------------------------------------------------------------------------
// ExecConfig.Stdin
// ---------------------------------------------------------------------------

func contractStdinReachesProcess() Contract {
	return Contract{
		Name: "exec/stdin_reaches_the_process",
		Why:  "ExecConfig.Stdin doc: \"when non-nil, is streamed to the command's standard input\". #1779: apple built the argv without -i, so the CLI attached nothing, the preflight script ran zero of its steps and the exec still exited 0.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if len(rt.EchoStdinCmd) == 0 {
				return res.skip("Runtime.EchoStdinCmd is not set")
			}
			const payload = "crewship-contract-stdin-payload"
			out, _, err := execAndRead(ctx, rt, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.EchoStdinCmd,
				User:        rt.safeUser(),
				Stdin:       strings.NewReader(payload),
			})
			if err != nil {
				res.violate("Exec with a non-nil Stdin returned an error: %v", err)
				return res
			}
			if !strings.Contains(out, payload) {
				res.violate("the process echoed %q; the %d bytes on ExecConfig.Stdin never reached its stdin", out, len(payload))
			}
			return res
		},
	}
}

func contractStdinHalfClosed() Contract {
	return Contract{
		Name: "exec/stdin_write_side_is_half_closed",
		Why:  "ExecConfig.Stdin doc: \"the write side is then half-closed so the process observes EOF\". Without it a process that reads to EOF (every shell fed a script) never exits, and the run hangs instead of failing.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if len(rt.EchoStdinCmd) == 0 {
				return res.skip("Runtime.EchoStdinCmd is not set")
			}
			_, execID, err := execAndRead(ctx, rt, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.EchoStdinCmd,
				User:        rt.safeUser(),
				Stdin:       strings.NewReader("crewship-contract-eof-probe"),
			})
			if err != nil {
				res.violate("Exec with a non-nil Stdin returned an error: %v", err)
				return res
			}
			code, err := waitExecFinished(ctx, rt.Provider, execID, 20*time.Second)
			if err != nil {
				res.violate("the stdin-reading process never finished: %v (the harness's echo command exits non-zero when stdin never reaches EOF, so a hang here means the write side was never half-closed)", err)
				return res
			}
			if code != 0 {
				res.violate("the stdin-reading process exited %d; by harness convention that is \"stdin never reached EOF\" — the write side was not half-closed", code)
			}
			return res
		},
	}
}

func contractNilStdinNotAttached() Contract {
	return Contract{
		Name:     "exec/nil_stdin_attaches_nothing",
		FakeOnly: true,
		Why:      "ExecConfig.Stdin doc: \"nil (the default) means no stdin is attached — byte-for-byte the historic behaviour\". Attaching a stream a command has nothing to read from changes how the runtime treats it (docker keeps the connection open; apple's -i changes the CLI's stream handling).",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.AttachedStdin == nil {
				return res.skip("Runtime.AttachedStdin is not observable")
			}
			if len(rt.EchoStdinCmd) == 0 {
				return res.skip("Runtime.EchoStdinCmd is not set")
			}
			if _, _, err := execAndRead(ctx, rt, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.EchoStdinCmd,
				User:        rt.safeUser(),
				// Stdin intentionally nil.
			}); err != nil {
				res.violate("Exec with a nil Stdin returned an error: %v", err)
				return res
			}
			if rt.AttachedStdin() {
				res.violate("ExecConfig.Stdin was nil but the provider still asked the runtime to attach stdin")
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// Privileged-exec guard (#1158)
// ---------------------------------------------------------------------------

// privilegedUsers are the forms provider.IsPrivilegedExecUser rejects, in the
// shapes a real call site could plausibly produce. docker pins exactly these in
// exec_fail_closed_test.go; they are lifted here so every provider answers for
// them, not just the one whose package the test happens to live in.
var privilegedUsers = []string{"0", "0:0", "root", "1001:0", "0:1001", " 0 ", "toor"}

func contractRefusesPrivilegedUser() Contract {
	return Contract{
		Name: "exec/refuses_privileged_user",
		Why:  "docker.go:1399 (#1158) — \"fail closed on empty OR root regardless of how the user arrives\". provider.IsPrivilegedExecUser defines the vocabulary; the guard must fire BEFORE the runtime is touched. apple ignored AllowPrivileged entirely and let `container exec --user root` through.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			for _, user := range privilegedUsers {
				before := runtimeCalls(rt)
				result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
					ContainerID: rt.ContainerID,
					Cmd:         rt.ExitCmd(0),
					User:        user,
					// AllowPrivileged intentionally unset: this is the default
					// path, and the default must refuse.
				})
				if err == nil {
					res.violate("Exec as %q succeeded; a privileged user must be refused unless ExecConfig.AllowPrivileged is set", user)
					closeExec(result)
					continue
				}
				if after := runtimeCalls(rt); after != before {
					res.violate("Exec as %q was refused, but the provider had already invoked the runtime (%d calls, was %d) — the guard must fail closed before the command runs", user, after, before)
				}
			}
			return res
		},
	}
}

func contractEmptyUserResolvesNonRoot() Contract {
	return Contract{
		Name:     "exec/empty_user_resolves_to_a_non_root_user",
		FakeOnly: true,
		Why:      "docker.go:1381-1389 (#1158) — an empty User must be resolved to the container's real configured run-as user and refused if that cannot be proven non-root. Leaving it empty lets the runtime pick the image default, which is root on nearly every base image.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.ExecUser == nil {
				return res.skip("Runtime.ExecUser is not observable")
			}
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.ExitCmd(0),
				// User intentionally empty.
			})
			if err != nil {
				// A provider that cannot prove a safe user must refuse — that
				// is the fail-closed half and is also conforming.
				return res
			}
			// Drain before observing: a provider that shells out to a CLI has
			// only STARTED the process when Exec returns, and the harness reads
			// its argv from what that process recorded. Reading the hook too
			// early sees an empty log and reports a violation that is the
			// test's own race, not the provider's.
			drainExecResult(result)
			seen := rt.ExecUser()
			if seen == "" {
				res.violate("Exec ran with no user at all; the runtime falls back to the image default (root on nearly every base image)")
				return res
			}
			if provider.IsPrivilegedExecUser(seen) {
				res.violate("Exec resolved the empty User to %q, which provider.IsPrivilegedExecUser rejects", seen)
			}
			return res
		},
	}
}

func contractAllowPrivilegedOptIn() Contract {
	return Contract{
		Name: "exec/allow_privileged_is_honoured",
		Why:  "ExecConfig.AllowPrivileged doc — the audited opt-in for the handful of orchestrator preflight steps that legitimately need root (killing a stale sidecar, pre-creating dual-writer files). The guard is a default, not an absolute; a provider that ignores the field breaks those steps.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
				ContainerID:     rt.ContainerID,
				Cmd:             rt.ExitCmd(0),
				User:            "0:0",
				AllowPrivileged: true,
			})
			if err != nil {
				res.violate("Exec as 0:0 with AllowPrivileged=true was refused: %v", err)
				return res
			}
			drainExecResult(result) // see the note in the empty-user contract
			if rt.ExecUser != nil {
				if seen := rt.ExecUser(); seen != "0:0" {
					res.violate("AllowPrivileged exec ran as %q, want the requested 0:0 passed through", seen)
				}
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// ExecResult shape
// ---------------------------------------------------------------------------

func contractExecResultShape() Contract {
	return Contract{
		Name: "exec/result_carries_an_exec_id_and_a_readable_stream",
		Why:  "ExecResult doc — \"holds the exec ID and output stream\". Every caller feeds ExecID straight to ExecInspect; an empty one makes the exit code unknowable, and a nil Reader panics the caller.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.ExitCmd(0),
				User:        rt.safeUser(),
			})
			if err != nil {
				res.violate("Exec returned an error: %v", err)
				return res
			}
			defer closeExec(result)
			if result == nil {
				res.violate("Exec returned (nil, nil)")
				return res
			}
			if result.ExecID == "" {
				res.violate("ExecResult.ExecID is empty; the caller cannot ask ExecInspect for the exit code")
			}
			if result.Reader == nil {
				res.violate("ExecResult.Reader is nil")
			}
			return res
		},
	}
}

func contractStderrInOutput() Contract {
	return Contract{
		Name: "exec/stderr_reaches_the_output_stream",
		Why:  "ExecResult exposes one \"output stream\", and the orchestrator surfaces it as the step's output. A provider that drops stderr loses every diagnostic a failing command prints, which is how a broken step reads as a silent one.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if len(rt.StderrCmd) == 0 || rt.StderrText == "" {
				return res.skip("Runtime.StderrCmd/StderrText are not set")
			}
			out, _, err := execAndRead(ctx, rt, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.StderrCmd,
				User:        rt.safeUser(),
			})
			if err != nil {
				res.violate("Exec returned an error: %v", err)
				return res
			}
			if !strings.Contains(out, rt.StderrText) {
				res.violate("output was %q; the command's stderr (%q) is missing from ExecResult.Reader", out, rt.StderrText)
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// ExecInspect
// ---------------------------------------------------------------------------

func contractInspectReportsRunning() Contract {
	return Contract{
		Name: "execinspect/reports_running_while_the_process_runs",
		Why:  "ExecInspect's first return value is the running flag; pipeline/runner_script.go:414 polls on it. Reporting false for a live process makes the caller read a half-finished command as a finished one.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if len(rt.BlockCmd) == 0 || rt.Unblock == nil {
				return res.skip("Runtime.BlockCmd/Unblock are not set")
			}
			defer rt.Unblock()
			result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.BlockCmd,
				User:        rt.safeUser(),
			})
			if err != nil {
				res.violate("Exec returned an error: %v", err)
				return res
			}
			defer closeExec(result)
			running, _, err := rt.Provider.ExecInspect(ctx, result.ExecID)
			if err != nil {
				res.violate("ExecInspect on a live exec returned an error: %v", err)
				return res
			}
			if !running {
				res.violate("ExecInspect reported running=false for a process that has not exited")
			}
			return res
		},
	}
}

func contractInspectRunningNotSuccess() Contract {
	return Contract{
		Name: "execinspect/running_is_not_mistakable_for_success",
		Why:  "Three call sites discard the running flag and branch on the code alone — server/routes_files.go:368 and :450 (`_, code, ierr :=`) and api/keeper_execute.go:697. For them a 0 returned while the process is still running IS success. apple/apple_exec.go:19 encodes this as execRunningExitCode = -1.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if len(rt.BlockCmd) == 0 || rt.Unblock == nil {
				return res.skip("Runtime.BlockCmd/Unblock are not set")
			}
			defer rt.Unblock()
			result, err := rt.Provider.Exec(ctx, provider.ExecConfig{
				ContainerID: rt.ContainerID,
				Cmd:         rt.BlockCmd,
				User:        rt.safeUser(),
			})
			if err != nil {
				res.violate("Exec returned an error: %v", err)
				return res
			}
			defer closeExec(result)
			running, code, err := rt.Provider.ExecInspect(ctx, result.ExecID)
			if err != nil {
				res.violate("ExecInspect on a live exec returned an error: %v", err)
				return res
			}
			if running && code == 0 {
				res.violate("ExecInspect returned exit code 0 for a still-running process; every caller that discards the running flag reads that as success")
			}
			return res
		},
	}
}

func contractInspectReportsExitCode() Contract {
	return Contract{
		Name: "execinspect/reports_the_real_exit_code_after_completion",
		Why:  "The exit code is the only thing distinguishing a preflight step that worked from one that failed. apple/apple_exec.go:150-158 records it off ProcessState precisely because stamping a placeholder threw away a status the kernel had already given us.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			for _, want := range []int{0, 7} {
				_, execID, err := execAndRead(ctx, rt, provider.ExecConfig{
					ContainerID: rt.ContainerID,
					Cmd:         rt.ExitCmd(want),
					User:        rt.safeUser(),
				})
				if err != nil {
					res.violate("Exec returned an error: %v", err)
					continue
				}
				got, err := waitExecFinished(ctx, rt.Provider, execID, 20*time.Second)
				if err != nil {
					res.violate("exec that should have exited %d never reported completion: %v", want, err)
					continue
				}
				if got != want {
					res.violate("ExecInspect reported exit code %d for a process that exited %d", got, want)
				}
			}
			return res
		},
	}
}

func contractInspectUnknownExecErrors() Contract {
	return Contract{
		Name: "execinspect/unknown_exec_id_is_an_error_not_a_success",
		Why:  "Same hazard as the running case: callers that keep only the code turn a (false, 0, nil) for an id the provider has never heard of into \"the command succeeded\". Either return an error or a non-success code — never both-nil.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			running, code, err := rt.Provider.ExecInspect(ctx, "crewship-contract-no-such-exec-id")
			if err == nil && code == 0 {
				res.violate("ExecInspect on an unknown exec id returned (running=%v, code=0, err=nil) — indistinguishable from a command that succeeded", running)
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// Naming
// ---------------------------------------------------------------------------

func contractCrewContainerNameKeyedByID() Contract {
	return Contract{
		Name: "naming/crew_container_name_is_keyed_by_crew_id",
		Why:  "CrewContainerName doc (container.go:254-258, audit C1) — \"keyed by the globally-unique crew id (not the per-workspace slug alone) so two tenants with an identically-named crew never collide on a shared daemon. The slug is retained as a human-readable name segment.\"",
		Check: func(_ context.Context, rt Runtime) Result {
			var res Result
			const slug = "contract-ops"
			a := rt.Provider.CrewContainerName("crew-aaaaaaaa", slug)
			b := rt.Provider.CrewContainerName("crew-bbbbbbbb", slug)
			if a == b {
				res.violate("two crews with the same slug but different ids both map to %q — a shared daemon would have them collide", a)
			}
			if again := rt.Provider.CrewContainerName("crew-aaaaaaaa", slug); again != a {
				res.violate("CrewContainerName is not deterministic: %q then %q for the same (id, slug)", a, again)
			}
			if !strings.Contains(a, slug) {
				res.violate("name %q does not retain the slug as a human-readable segment", a)
			}
			if !strings.Contains(a, "crew-aaaaaaaa") {
				res.violate("name %q does not contain the crew id it is supposed to be keyed by", a)
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// Optional interface: InteractiveExecProvider
// ---------------------------------------------------------------------------

func contractInteractiveRefusesPrivileged() Contract {
	return Contract{
		Name: "execinteractive/refuses_privileged_user",
		Why:  "docker.go:1473 (#1158) — the interactive path is the web terminal, reachable from a request, so InteractiveExecConfig has no AllowPrivileged field at all and a privileged user is refused outright.",
		Check: func(ctx context.Context, rt Runtime) Result {
			var res Result
			ip, ok := rt.Provider.(provider.InteractiveExecProvider)
			if !ok {
				return res.skip("provider does not implement provider.InteractiveExecProvider")
			}
			if rt.ExitCmd == nil {
				return res.skip("Runtime.ExitCmd is not set")
			}
			for _, user := range privilegedUsers {
				before := runtimeCalls(rt)
				result, err := ip.ExecInteractive(ctx, provider.InteractiveExecConfig{
					ContainerID: rt.ContainerID,
					Cmd:         rt.ExitCmd(0),
					User:        user,
				})
				if err == nil {
					res.violate("ExecInteractive as %q succeeded; the web-terminal path has no privileged opt-in", user)
					if result != nil && result.Conn != nil {
						_ = result.Conn.Close()
					}
					continue
				}
				if after := runtimeCalls(rt); after != before {
					res.violate("ExecInteractive as %q was refused, but the provider had already invoked the runtime (%d calls, was %d)", user, after, before)
				}
			}
			return res
		},
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func runtimeCalls(rt Runtime) int {
	if rt.RuntimeCalls == nil {
		return 0
	}
	return rt.RuntimeCalls()
}

// drainExecResult reads an exec's output to EOF and closes it, which is the
// only portable way to know the process has actually finished — the point after
// which a fake runtime's recorded argv is guaranteed to be complete. Bounded,
// so a stream that never closes fails the contract rather than the test binary.
func drainExecResult(r *provider.ExecResult) {
	if r == nil || r.Reader == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, r.Reader)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	_ = r.Reader.Close()
}

func closeExec(r *provider.ExecResult) {
	if r != nil && r.Reader != nil {
		_ = r.Reader.Close()
	}
}

// execAndRead runs one exec and drains its output stream, returning the output
// and the exec id. The read is bounded: a provider that never closes the stream
// is a production hang, and it has to surface here as a failure rather than as
// a wedged test binary.
func execAndRead(ctx context.Context, rt Runtime, cfg provider.ExecConfig) (string, string, error) {
	result, err := rt.Provider.Exec(ctx, cfg)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", fmt.Errorf("Exec returned (nil, nil)")
	}
	if result.Reader == nil {
		return "", result.ExecID, nil
	}
	type readResult struct {
		out []byte
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		out, err := io.ReadAll(result.Reader)
		done <- readResult{out, err}
	}()
	select {
	case rr := <-done:
		_ = result.Reader.Close()
		if rr.err != nil {
			return string(rr.out), result.ExecID, fmt.Errorf("read exec output: %w", rr.err)
		}
		return string(rr.out), result.ExecID, nil
	case <-time.After(20 * time.Second):
		_ = result.Reader.Close()
		return "", result.ExecID, fmt.Errorf("exec output stream never reached EOF")
	case <-ctx.Done():
		_ = result.Reader.Close()
		return "", result.ExecID, ctx.Err()
	}
}

// waitExecFinished polls ExecInspect until the process reports finished, and
// returns its exit code. Polling (rather than a single call) is deliberate: the
// contract is that the code is EVENTUALLY real, not that it is available on the
// first inspect.
func waitExecFinished(ctx context.Context, p provider.ContainerProvider, execID string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		running, code, err := p.ExecInspect(ctx, execID)
		switch {
		case err != nil:
			lastErr = err
		case !running:
			return code, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return 0, fmt.Errorf("still running after %s (last ExecInspect error: %w)", timeout, lastErr)
	}
	return 0, fmt.Errorf("still running after %s", timeout)
}
