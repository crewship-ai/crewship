//go:build unix

package runoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type hostFixtureExecutor struct {
	mu             sync.Mutex
	starts         int
	stops          int
	running        bool
	code           int
	stdout, stderr string
	inspectErr     error
}

func (f *hostFixtureExecutor) Start(context.Context, HostCommand) (*HostExecution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return &HostExecution{ID: "owned-exec", Stdout: io.NopCloser(strings.NewReader(f.stdout)), Stderr: io.NopCloser(strings.NewReader(f.stderr))}, nil
}
func (f *hostFixtureExecutor) Inspect(context.Context, string) (bool, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.code, f.inspectErr
}
func (f *hostFixtureExecutor) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "owned-exec" {
		return errors.New("wrong exec")
	}
	f.stops++
	f.running = false
	f.code = 137
	return nil
}
func hostFixtureInput(t *testing.T) HostInput {
	t.Helper()
	if uid := os.Geteuid(); os.Getenv("CREWSHIP_HOST_CAPTURE_REQUIRE_UNPRIVILEGED") == "1" && (uid == 0 || uid == 1001 || uid == 1002) {
		t.Fatal("required capture qualification needs distinct unprivileged host UID")
	}
	if uid := os.Geteuid(); uid == 1001 || uid == 1002 {
		t.Skip("protected capture requires a host UID distinct from workload/sidecar; required qualification runs under a distinct unprivileged UID")
	}
	return hostFixtureInputUnchecked(t)
}
func hostFixtureInputUnchecked(t *testing.T) HostInput {
	t.Helper()
	return HostInput{Directory: filepath.Join(t.TempDir(), "run"), Deadline: time.Now().Add(5 * time.Second), MaxBytes: DefaultLimit, Command: HostCommand{ContainerID: "container", User: "1001:1001", Args: []string{"/fixture", "literal; $(never)"}}}
}
func createHostFixtureDir(t *testing.T, input HostInput) {
	t.Helper()
	if err := os.Mkdir(input.Directory, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestHostCaptureSeparatesStreamsScrubsAndRejectsDuplicate(t *testing.T) {
	input := hostFixtureInput(t)
	createHostFixtureDir(t, input)
	secret := strings.Repeat("private-token-", 30)
	input.Secrets = []string{secret}
	executor := &hostFixtureExecutor{code: 7, stdout: "{\"text\":\"" + secret + "\"}\n", stderr: "diagnostic\n"}
	result, err := CaptureHost(t.Context(), input, executor)
	if err != nil || result.ExitCode != 7 || result.Reason != "exited" {
		t.Fatalf("capture=%+v %v", result, err)
	}
	var stdout, stderr strings.Builder
	_, complete, err := ReadCommitted(filepath.Join(input.Directory, "output"), 0, func(record Record) error {
		if record.Kind == "output" {
			if record.Stream == "stdout" {
				stdout.Write(record.Data)
			} else {
				stderr.Write(record.Data)
			}
		}
		return nil
	})
	if err != nil || !complete || strings.Contains(stdout.String(), secret) || !strings.Contains(stdout.String(), "[REDACTED") || stderr.String() != "diagnostic\n" {
		t.Fatalf("streams stdout=%q stderr=%q complete=%v err=%v", stdout.String(), stderr.String(), complete, err)
	}
	if _, err = CaptureHost(t.Context(), input, executor); !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate=%v", err)
	}
	if executor.starts != 1 {
		t.Fatalf("launch count=%d", executor.starts)
	}
	identity, err := os.ReadFile(filepath.Join(input.Directory, "identity.json"))
	if err != nil || bytes.Contains(identity, []byte(secret)) || !bytes.Contains(identity, []byte("owned-exec")) {
		t.Fatalf("identity=%s %v", identity, err)
	}
}

func TestHostCaptureOverflowStopsOnlyOwnedExecutionAndRetainsTerminal(t *testing.T) {
	input := hostFixtureInput(t)
	createHostFixtureDir(t, input)
	input.MaxBytes = terminalReserve + chunkBytes*2
	executor := &hostFixtureExecutor{running: true, stdout: strings.Repeat("x", chunkBytes*8)}
	result, err := CaptureHost(t.Context(), input, executor)
	if err != nil || result.Reason != "output_limit" || executor.stops != 1 {
		t.Fatalf("result=%+v stops=%d err=%v", result, executor.stops, err)
	}
	snapshot, err := Inspect(t.Context(), filepath.Join(input.Directory, "output"))
	if err != nil || !snapshot.Complete || snapshot.Result.Reason != "output_limit" {
		t.Fatalf("snapshot=%+v %v", snapshot, err)
	}
	info, err := os.Stat(filepath.Join(input.Directory, "output", "events.jsonl"))
	if err != nil || info.Size() > input.MaxBytes {
		t.Fatalf("spool exceeds limit: %v %v", info, err)
	}
}

func TestHostCaptureRefusesWorkloadWritableOrSymlinkDirectory(t *testing.T) {
	for _, kind := range []string{"public", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			input := hostFixtureInputUnchecked(t)
			createHostFixtureDir(t, input)
			if kind == "public" {
				if err := os.Chmod(input.Directory, 0777); err != nil {
					t.Fatal(err)
				}
			} else {
				actual := input.Directory
				input.Directory = filepath.Join(filepath.Dir(actual), "alias")
				if err := os.Symlink(actual, input.Directory); err != nil {
					t.Fatal(err)
				}
			}
			executor := &hostFixtureExecutor{}
			if _, err := CaptureHost(t.Context(), input, executor); err == nil {
				t.Fatal("unsafe host directory admitted")
			}
			if executor.starts != 0 {
				t.Fatal("unsafe directory started workload")
			}
		})
	}
}

// This executor exists only in the subprocess fixture. The production helper
// uses its independently admitted Docker adapter, not arbitrary host commands.
type hostLocalExecutor struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	done bool
	code int
}

func (f *hostLocalExecutor) Start(ctx context.Context, c HostCommand) (*HostExecution, error) {
	f.cmd = exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	f.cmd.Env = c.Env
	stdout, err := f.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := f.cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = f.cmd.Start(); err != nil {
		return nil, err
	}
	return &HostExecution{ID: "local-exec", Stdout: stdout, Stderr: stderr}, nil
}
func (f *hostLocalExecutor) Inspect(context.Context, string) (bool, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.done {
		_ = f.cmd.Wait()
		f.done = true
		f.code = f.cmd.ProcessState.ExitCode()
	}
	return false, f.code, nil
}
func (f *hostLocalExecutor) Stop(context.Context, string) error { return f.cmd.Process.Kill() }

func TestHostCaptureSubprocess(t *testing.T) {
	if os.Getenv("CREWSHIP_HOST_CAPTURE_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			input, err := ReadHostInput(os.Stdin)
			if err != nil {
				os.Exit(126)
			}
			if os.Args[i+1] == "helper" {
				_, err = CaptureHost(context.Background(), input, &hostLocalExecutor{})
				if err != nil {
					os.Exit(125)
				}
				os.Exit(0)
			}
			executable, _ := os.Executable()
			helper := []string{executable, "-test.run=^TestHostCaptureSubprocess$", "--", "helper"}
			_, err = StartHost(context.Background(), input, helper, []string{"CREWSHIP_HOST_CAPTURE_TEST_CHILD=1"})
			if err != nil {
				os.Exit(124)
			}
			// The parent kills this controller after observing captured first output.
			select {}
		}
	}
	os.Exit(123)
}

func TestHostCaptureSurvivesActualControllerKill(t *testing.T) {
	input := hostFixtureInput(t)
	input.Deadline = time.Now().Add(15 * time.Second)
	root := filepath.Dir(input.Directory)
	release, starts := filepath.Join(root, "release"), filepath.Join(root, "starts")
	input.Command.Args = []string{"/bin/sh", "-c", `printf x >> "$2"; printf 'before\n'; while [ ! -f "$1" ]; do /bin/sleep 0.02; done; printf 'after\n'; exit 7`, "fixture", release, starts}
	// Large initial output flushes the scrubber overlap while the workload waits.
	input.Command.Args[2] = strings.Replace(input.Command.Args[2], "printf 'before\\n'", "printf 'before\\n'; printf '%0200d\\n' 0", 1)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	controller := exec.Command(executable, "-test.run=^TestHostCaptureSubprocess$", "--", "controller")
	controller.Env = []string{"CREWSHIP_HOST_CAPTURE_TEST_CHILD=1"}
	controller.Stdin = bytes.NewReader(data)
	if err = controller.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = controller.Process.Kill(); _ = controller.Wait(); _ = os.WriteFile(release, nil, 0600) }()
	waitUntil := func(check func() bool) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if check() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("host capture condition timed out")
	}
	waitUntil(func() bool {
		data, _ := os.ReadFile(filepath.Join(input.Directory, "output", "events.jsonl"))
		return bytes.Contains(data, []byte(`"kind":"output"`))
	})
	if err = controller.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = controller.Wait()
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitUntil(func() bool {
		snapshot, err := Inspect(t.Context(), filepath.Join(input.Directory, "output"))
		return err == nil && snapshot.Complete
	})
	snapshot, err := Inspect(t.Context(), filepath.Join(input.Directory, "output"))
	if err != nil || snapshot.Result.ExitCode != 7 {
		t.Fatalf("lost terminal=%+v %v", snapshot, err)
	}
	_, err = StartHost(t.Context(), input, []string{executable}, nil)
	if !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate controller launch=%v", err)
	}
	count, err := os.ReadFile(starts)
	if err != nil || string(count) != "x" {
		t.Fatalf("duplicate workload count=%q %v", count, err)
	}
}

func TestHostCaptureInspectFailureNeverInventsTerminal(t *testing.T) {
	input := hostFixtureInput(t)
	createHostFixtureDir(t, input)
	want := errors.New("daemon unavailable")
	executor := &hostFixtureExecutor{stdout: "structured output\n", inspectErr: want}
	if _, err := CaptureHost(t.Context(), input, executor); !errors.Is(err, want) {
		t.Fatalf("inspect uncertainty lost: %v", err)
	}
	snapshot, err := Inspect(t.Context(), filepath.Join(input.Directory, "output"))
	if err != nil || snapshot.Complete || snapshot.Result != nil {
		t.Fatalf("invented terminal on inspect failure: %+v %v", snapshot, err)
	}
}

type hostLateCompletionExecutor struct {
	*hostFixtureExecutor
	cancel context.CancelFunc
}

func (e hostLateCompletionExecutor) Inspect(context.Context, string) (bool, int, error) {
	e.cancel()
	return false, 0, nil
}

func TestHostCaptureNaturalCompletionWinsLateCancellation(t *testing.T) {
	input := hostFixtureInput(t)
	createHostFixtureDir(t, input)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture := &hostFixtureExecutor{stdout: "completed\n"}
	result, err := CaptureHost(ctx, input, hostLateCompletionExecutor{fixture, cancel})
	if err != nil || result.Reason != "exited" || result.ExitCode != 0 || fixture.stops != 0 {
		t.Fatalf("late cancellation replaced completion: %+v stops=%d err=%v", result, fixture.stops, err)
	}
}

func TestHostCaptureRefusesWorkloadHostUID(t *testing.T) {
	if uid := os.Geteuid(); uid != 1001 && uid != 1002 {
		t.Skip("refusal applies to workload and sidecar host UIDs")
	}
	input := hostFixtureInputUnchecked(t)
	createHostFixtureDir(t, input)
	executor := &hostFixtureExecutor{}
	if _, err := CaptureHost(t.Context(), input, executor); err == nil {
		t.Fatal("workload/sidecar host UID admitted")
	}
	if executor.starts != 0 {
		t.Fatal("unsafe UID created execution")
	}
}
