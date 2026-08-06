package apple

// The Apple provider's run of the shared, provider-agnostic contract suite
// (internal/provider/providertest).
//
// This provider is the reason the suite exists. Its Exec built the CLI argv
// without -i, so the caller's stdin was never attached: `sh` read an
// already-EOF stream, executed none of the merged preflight batch, and exited
// 0 — six steps reported success having done nothing, and the run died three
// layers later on a missing .mcp.json (#1779). It also read ExecConfig.User and
// ExecConfig.AllowPrivileged nowhere, so the #1158 fail-closed guard docker had
// pinned for months simply did not exist on macOS. Both contracts were written
// down — in the docker package's own test directory, where nobody writing a
// second provider would look.
//
// The fake `container` binary below is faithful on the point that mattered: it
// forwards stdin ONLY when -i is present, exactly as container 1.2.0 does.
// A provider that streams the bytes but forgets the flag fails here, which is
// the regression that shipped.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/providertest"
)

func TestAppleProvider_ContractSuite(t *testing.T) {
	providertest.RunContractSuite(t, newAppleContractRuntime)
}

const contractStderrText = "contract-stderr-marker"

// contractFakeCLI is a stub `container` binary on PATH. It logs every
// invocation and implements the contract suite's command vocabulary.
type contractFakeCLI struct {
	dir     string
	log     string
	unblock string
}

// contractFakeCLIScript models the parts of `container exec` the contracts
// depend on. The -i handling is the load-bearing part: verified against
// container 1.2.0,
//
//	printf 'echo HELLO\nexit 7\n' | container exec    <id> sh  ->  no output, exit 0
//	printf 'echo HELLO\nexit 7\n' | container exec -i <id> sh  ->  HELLO,     exit 7
//
// so a fake that always forwarded stdin would pass a provider that omits the
// flag — i.e. it would have passed the #1779 bug.
//
// It also answers `inspect`, because the provider now reads a container's
// run-as user from the runtime instead of returning the create-time constant.
// The suite's empty-User contract counts a refusal as conforming, so a stub
// that stayed silent here would leave that contract passing on the refusal
// branch and never exercise the resolve at all.
const contractFakeCLIScript = `
if [ "$1" = "inspect" ]; then
  printf '[{"status":{"state":"running"},"configuration":{"id":"%s","initProcess":{"user":{"raw":{"userString":"` + agentContainerUser + `"}}}}}]' "$2"
  exit 0
fi
has_i=0
op=""
for a in "$@"; do
  case "$a" in
    -i|--interactive) has_i=1 ;;
    echo-stdin|stderr|block) op="$a" ;;
    exit-*) op="$a" ;;
  esac
done
case "$op" in
  echo-stdin)
    if [ "$has_i" = "1" ]; then cat; fi
    exit 0 ;;
  stderr)
    printf '%s' '` + contractStderrText + `' >&2
    exit 0 ;;
  block)
    while [ ! -f "$DIR/unblock" ]; do sleep 0.05; done
    exit 0 ;;
  exit-*)
    exit "${op#exit-}" ;;
esac
exit 0
`

func installContractFakeCLI(t *testing.T) *contractFakeCLI {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "contract-calls.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + logFile + "'\n" +
		"DIR='" + dir + "'\n" +
		"export DIR\n" +
		contractFakeCLIScript
	if err := os.WriteFile(filepath.Join(dir, "container"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake container CLI: %v", err)
	}
	// t.Setenv, so the suite's subtests must not be parallel — RunContractSuite
	// does not make them parallel for exactly this reason.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &contractFakeCLI{dir: dir, log: logFile, unblock: filepath.Join(dir, "unblock")}
}

// calls returns one entry per CLI invocation, space-joined argv.
func (f *contractFakeCLI) calls() []string {
	data, err := os.ReadFile(f.log)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (f *contractFakeCLI) lastCall() []string {
	calls := f.calls()
	if len(calls) == 0 {
		return nil
	}
	return strings.Fields(calls[len(calls)-1])
}

// releaseBlocked lets every `block` command finish.
func (f *contractFakeCLI) releaseBlocked() {
	_ = os.WriteFile(f.unblock, []byte("go"), 0o600)
}

// newContractProvider builds a Provider without New (which would run Detect and
// need a real host). Only the exec bookkeeping is required by this suite.
//
// Deliberately independent of the package's other test constructors: this file
// must keep asserting the shared contract even while the provider's own tests
// are being reworked around it.
func newContractProvider() *Provider {
	return &Provider{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		execs:  make(map[string]*execEntry),
		done:   make(chan struct{}),
		mounts: make(map[string]map[string]string),
	}
}

func newAppleContractRuntime(t *testing.T) providertest.Runtime {
	t.Helper()
	fake := installContractFakeCLI(t)
	t.Cleanup(fake.releaseBlocked)
	p := newContractProvider()

	return providertest.Runtime{
		Provider:     p,
		ContainerID:  "contract-container",
		SafeUser:     agentContainerUser,
		EchoStdinCmd: []string{"echo-stdin"},
		ExitCmd:      func(code int) []string { return []string{"exit-" + strconv.Itoa(code)} },
		StderrCmd:    []string{"stderr"},
		StderrText:   contractStderrText,
		BlockCmd:     []string{"block"},
		Unblock:      fake.releaseBlocked,
		AttachedStdin: func() bool {
			for _, arg := range fake.lastCall() {
				if arg == "-i" || arg == "--interactive" {
					return true
				}
			}
			return false
		},
		ExecUser: func() string {
			args := fake.lastCall()
			for i, arg := range args {
				if arg == "--user" && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		},
		RuntimeCalls: func() int { return len(fake.calls()) },
	}
}

// noInteractiveProvider is this provider's Exec as it shipped before #1779:
// identical in every respect except that it never passes -i, so the CLI
// attaches no stdin. Everything else is inherited from the real Provider.
type noInteractiveProvider struct{ *Provider }

func (p *noInteractiveProvider) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	user, err := p.resolveExecUser(ctx, "exec", cfg.ContainerID, cfg.User, cfg.AllowPrivileged)
	if err != nil {
		return nil, err
	}
	args := []string{"exec"} // the missing `-i` — the whole bug
	for _, env := range cfg.Env {
		args = append(args, "--env", env)
	}
	if cfg.WorkingDir != "" {
		args = append(args, "--workdir", cfg.WorkingDir)
	}
	args = append(args, "--user", user, cfg.ContainerID)
	args = append(args, cfg.Cmd...)

	cmd := exec.CommandContext(ctx, "container", args...)
	if cfg.Stdin != nil {
		cmd.Stdin = cfg.Stdin
	}
	spool := newExecSpool()
	cmd.Stdout = spool
	cmd.Stderr = spool
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	execID := p.registerExec(cmd, spool.closeWrite)
	return &provider.ExecResult{ExecID: execID, Reader: spool}, nil
}

// A contract check nobody has watched fail is indistinguishable from one that
// always passes — and "always passes" is exactly the state this package's tests
// were in while Exec discarded stdin. So: reintroduce the #1779 defect and
// require the shared suite to catch it.
func TestContractSuite_CatchesTheMissingInteractiveFlag(t *testing.T) {
	rt := newAppleContractRuntime(t)
	rt.Provider = &noInteractiveProvider{rt.Provider.(*Provider)}

	const name = "exec/stdin_reaches_the_process"
	var c providertest.Contract
	for _, candidate := range providertest.Contracts() {
		if candidate.Name == name {
			c = candidate
		}
	}
	if c.Check == nil {
		t.Fatalf("contract %q not found in the shared suite", name)
	}

	res := c.Check(t.Context(), rt)
	if res.Skipped != "" {
		t.Fatalf("contract %s skipped instead of running: %s", name, res.Skipped)
	}
	if len(res.Violations) == 0 {
		t.Errorf("contract %s passed against the pre-#1779 Exec (no -i, so the CLI attaches nothing) — the check has no teeth", name)
	}
}

// Compile-time proof that the provider under test still satisfies the interface
// the suite is written against.
var _ provider.ContainerProvider = (*Provider)(nil)
