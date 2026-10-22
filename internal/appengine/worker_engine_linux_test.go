//go:build linux

package appengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain lets the test binary act as the engine sandbox launcher, exactly as
// the worker binary does in its image.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == EngineSandboxFlag {
		err := ExecEngineSandbox(os.Args[2:])
		fmt.Fprintln(os.Stderr, "engine sandbox:", err)
		os.Exit(126)
	}
	if len(os.Args) == 2 && os.Args[1] == testSandboxProbeFlag {
		os.Exit(sandboxProbe())
	}
	if len(os.Args) == 4 && os.Args[1] == testLeaveGroupFlag {
		// Stands in for a CLI descendant that moves to a new process group
		// inside the engine's session (setpgid), then lingers.
		if err := syscall.Setpgid(0, 0); err != nil {
			os.Exit(90)
		}
		if err := os.WriteFile(os.Args[3], nil, 0600); err != nil {
			os.Exit(91)
		}
		_ = syscall.Exec("/usr/bin/sleep", []string{"sleep", os.Args[2]}, nil)
		os.Exit(92)
	}
	os.Exit(m.Run())
}

// fakeCLI writes a shell script that stands in for the pinned CLI. The
// runner always invokes `run --in SRC --out DST --quiet` for an import.
func fakeCLI(t *testing.T, body string) WorkerEngine {
	t.Helper()
	if err := PrepareEngineSandbox(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("engine sandbox unavailable in CI, containment would be unverified: %v", err)
		}
		t.Skipf("engine sandbox unavailable on this kernel: %v", err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-gridcraft")
	script := "#!/bin/sh\nset -u\nsrc=\"$3\"\ndst=\"$5\"\n" + body + "\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	return WorkerEngine{Binary: binary, Scratch: t.TempDir(), ReadOnly: []string{dir}, digest: hex.EncodeToString(sum[:])}
}

const testLeaveGroupFlag = "--test-leave-group"
const testSandboxProbeFlag = "--test-sandbox-probe"

// The Go runtime reserves more address space than the pinned CLI's default
// limit allows; test helpers written in Go get a larger one.
const goHelperAddressSpace = 4 << 30

// sandboxProbe runs inside the confined engine and reports, by exit code, the
// first process-control call that was not refused with EPERM.
func sandboxProbe() int {
	if _, err := unix.Setsid(); !errors.Is(err, unix.EPERM) {
		return 10
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); !errors.Is(err, unix.EPERM) {
		return 11
	}
	for _, flag := range []int{unix.CLONE_NEWUSER, unix.CLONE_NEWNS, unix.CLONE_NEWPID, unix.CLONE_NEWNET, unix.CLONE_NEWUTS, unix.CLONE_NEWIPC} {
		if err := unix.Unshare(flag); !errors.Is(err, unix.EPERM) {
			return 12
		}
	}
	// clone with a namespace flag must be refused before a child exists.
	if _, _, errno := unix.RawSyscall6(unix.SYS_CLONE, uintptr(unix.CLONE_NEWUSER|unix.CLONE_NEWPID|int(unix.SIGCHLD)), 0, 0, 0, 0, 0); errno != unix.EPERM {
		if errno == 0 {
			// A child was created; this branch also runs in it.
			unix.RawSyscall(unix.SYS_EXIT_GROUP, 13, 0, 0)
		}
		return 13
	}
	// x32 numbering (bit 30) on the native architecture is refused, not
	// merely unimplemented.
	if _, _, errno := unix.Syscall(0x40000000|unix.SYS_GETPID, 0, 0, 0); errno != unix.EPERM {
		return 15
	}
	if !foreignArchRefused() {
		return 16
	}
	// Ordinary process creation still works under the filter.
	if err := exec.Command("/usr/bin/true").Run(); err != nil {
		return 14
	}
	return 0
}

// sandboxProbeScript runs the probe binary inside the engine sandbox.
func sandboxProbeScript(t *testing.T, engine *WorkerEngine) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	engine.ReadOnly = append(engine.ReadOnly, filepath.Dir(self))
	engine.Limits.AddressSpace = goHelperAddressSpace
	return fmt.Sprintf(`if setsid true </dev/null >/dev/null 2>&1; then exit 9; fi
%q %s </dev/null >/dev/null 2>&1 || exit $?
cp "$src" "$dst"`, self, testSandboxProbeFlag)
}

// leaveGroupScript starts a descendant that leaves the engine's process group
// but stays in its session, and waits until it has done so.
func leaveGroupScript(t *testing.T, engine *WorkerEngine, marker string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	engine.ReadOnly = append(engine.ReadOnly, filepath.Dir(self))
	engine.Limits.AddressSpace = goHelperAddressSpace
	return fmt.Sprintf(`%q %s %s "$TMPDIR/left" </dev/null >/dev/null 2>&1 &
i=0; while [ ! -e "$TMPDIR/left" ]; do i=$((i+1)); [ $i -lt 500 ] || exit 70; sleep 0.01; done
cp "$src" "$dst"`, self, testLeaveGroupFlag, marker)
}

// sleepMarker returns a unique sleep duration so test processes can be found
// in /proc without matching unrelated host processes.
func sleepMarker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("30.%d", 1000000+int(b[0])<<16|int(b[1])<<8|int(b[2]))
	t.Cleanup(func() {
		for _, pid := range processesWithArg(marker) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return marker
}

func processesWithArg(arg string) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		var pid int
		if _, err := fmt.Sscanf(entry.Name(), "%d", &pid); err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		stat, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if i := strings.LastIndexByte(string(stat), ')'); i > 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
			continue
		}
		for _, field := range strings.Split(string(cmdline), "\x00") {
			if field == arg {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func requireGone(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pids := processesWithArg(marker)
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine descendant survived the candidate: pids %v", pids)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func blankWorkbook(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func importBlank(t *testing.T, ctx context.Context, engine WorkerEngine) (EngineResult, error) {
	raw := blankWorkbook(t)
	return engine.Run(ctx, raw, Command{Kind: "import", Snapshot: raw})
}

func TestWorkerEngineDetachedChildFailsClosedAndIsKilled(t *testing.T) {
	marker := sleepMarker(t)
	// The child leaves the engine's process group and is reparented when its
	// parent exits; the CLI reports success.
	engine := fakeCLI(t, "")
	engine = fakeCLI2(t, engine, leaveGroupScript(t, &engine, marker))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	_, err := importBlank(t, t.Context(), engine)
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("escaped engine descendant must fail closed, got %v", err)
	}
	if escapes.Load() != 1 {
		t.Fatalf("worker exit hook called %d times, want 1", escapes.Load())
	}
	requireGone(t, marker)
	if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*")); len(files) != 0 {
		t.Fatalf("scratch survived: %v", files)
	}
}

func TestWorkerHandlerQuarantinesAfterEngineEscape(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, "")
	engine = fakeCLI2(t, engine, leaveGroupScript(t, &engine, marker))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	secret := strings.Repeat("q", 64)
	handler, err := NewWorkerHandler(secret, engine, 1)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"snapshot":` + string(blankWorkbook(t)) + `,"command":{"kind":"import"}}`
	request := func(method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(workerServiceHeader, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := request(http.MethodPost, "/v1/candidate", body); code != http.StatusServiceUnavailable {
		t.Fatalf("escaping candidate status %d, want 503", code)
	}
	if code := request(http.MethodGet, "/v1/health", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("worker must stay unavailable after an escape, health %d", code)
	}
	if escapes.Load() != 1 {
		t.Fatalf("worker exit hook called %d times, want 1", escapes.Load())
	}
	requireGone(t, marker)
}

func TestWorkerEngineTimeoutKillsWholeProcessGroup(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `sleep `+marker+` & sleep `+marker)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := importBlank(t, ctx, engine)
	if err == nil {
		t.Fatal("timed out engine must fail")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout cleanup took %v", elapsed)
	}
	requireGone(t, marker)
	if escapes.Load() != 0 {
		t.Fatalf("in-group processes are not an escape, hook called %d times", escapes.Load())
	}
}

func TestWorkerEngineSuccessReapsInGroupLeftovers(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `sleep `+marker+` </dev/null >/dev/null 2>&1 & cp "$src" "$dst"`)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	result, err := importBlank(t, t.Context(), engine)
	if err != nil || len(result.Result) == 0 {
		t.Fatalf("result=%d bytes err=%v", len(result.Result), err)
	}
	requireGone(t, marker)
	if escapes.Load() != 0 {
		t.Fatalf("in-group leftovers are killed, not an escape; hook called %d times", escapes.Load())
	}
}

func TestWorkerEngineCannotReachSecretSocketOrOtherCandidates(t *testing.T) {
	private := t.TempDir()
	secret := filepath.Join(private, "secrets", "service-token")
	socketDir := filepath.Join(private, "worker")
	for _, dir := range []string{filepath.Dir(secret), socketDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secret, []byte(strings.Repeat("k", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	placeholder := filepath.Join(socketDir, "service.sock")
	if err := os.WriteFile(placeholder, nil, 0600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`
if cat %[1]q >/dev/null 2>&1; then exit 71; fi
if ls %[2]q >/dev/null 2>&1; then exit 72; fi
if cat "$src" >/dev/null 2>&1 && ls "$(dirname "$(dirname "$src")")" >/dev/null 2>&1; then exit 73; fi
if cat /proc/$PPID/environ >/dev/null 2>&1; then exit 74; fi
rm -f %[3]q 2>/dev/null
touch %[2]q/listener 2>/dev/null
echo tampered | tee "$(dirname "$(dirname "$src")")/workbook-other/candidate.json" >/dev/null 2>&1
cp "$src" "$dst"`, secret, socketDir, placeholder)
	engine := fakeCLI(t, body)
	other := filepath.Join(engine.Scratch, "workbook-other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	otherCandidate := filepath.Join(other, "candidate.json")
	if err := os.WriteFile(otherCandidate, []byte(`{"private":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := importBlank(t, t.Context(), engine); err != nil {
		t.Fatalf("sandboxed engine reached worker-private state or could not use its own scratch: %v", err)
	}
	if _, err := os.Stat(placeholder); err != nil {
		t.Fatalf("engine removed the service socket: %v", err)
	}
	if _, err := os.Stat(filepath.Join(socketDir, "listener")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("engine created a file in the socket directory: %v", err)
	}
	if got, _ := os.ReadFile(otherCandidate); string(got) != `{"private":true}` {
		t.Fatalf("engine tampered with another candidate: %q", got)
	}
}

// gatedScript blocks until gate exists, so a test can keep a candidate in
// flight while another candidate finishes and is swept.
func gatedScript(gate, rest string) string {
	return fmt.Sprintf("while [ ! -e %q ]; do sleep 0.01; done\n%s", gate, rest)
}

func gateDir(t *testing.T, engines ...*WorkerEngine) string {
	t.Helper()
	dir := t.TempDir()
	for _, engine := range engines {
		engine.ReadOnly = append(engine.ReadOnly, dir)
	}
	return filepath.Join(dir, "open")
}

func openGate(t *testing.T, gate string) {
	t.Helper()
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

// Concurrent healthy candidates whose in-group leftovers die during another
// candidate's sweep must not be mistaken for escapes.
func TestWorkerEngineConcurrentHealthyRunsAreNotEscapes(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `sleep `+marker+` </dev/null >/dev/null 2>&1 &
sleep `+marker+` </dev/null >/dev/null 2>&1 &
cp "$src" "$dst"`)
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	const parallel, rounds = 6, 8
	errs := make(chan error, parallel*rounds)
	var wg sync.WaitGroup
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				result, err := importBlank(t, t.Context(), engine)
				if err == nil && len(result.Result) == 0 {
					err = errors.New("empty result")
				}
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("healthy concurrent candidate failed: %v", err)
		}
	}
	if escapes.Load() != 0 {
		t.Fatalf("healthy concurrent candidates reported %d escapes", escapes.Load())
	}
	requireGone(t, marker)
	if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*")); len(files) != 0 {
		t.Fatalf("scratch survived: %v", files)
	}
}

// A crashing candidate fails alone; the concurrent healthy one succeeds.
func TestWorkerEngineCrashFailsOnlyItsOwnCandidate(t *testing.T) {
	marker := sleepMarker(t)
	good := fakeCLI(t, `cp "$src" "$dst"`)
	bad := fakeCLI(t, `sleep `+marker+` </dev/null >/dev/null 2>&1 &
kill -SEGV $$`)
	gate := gateDir(t, &good)
	good = fakeCLI2(t, good, gatedScript(gate, `sleep `+marker+` </dev/null >/dev/null 2>&1 &
cp "$src" "$dst"`))
	var escapes atomic.Int32
	good.Escaped = func() { escapes.Add(1) }
	bad.Escaped = good.Escaped
	goodDone := make(chan error, 1)
	go func() { _, err := importBlank(t, t.Context(), good); goodDone <- err }()
	if _, err := importBlank(t, t.Context(), bad); err == nil || errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("crashed candidate must fail as a candidate error, got %v", err)
	}
	openGate(t, gate)
	if err := <-goodDone; err != nil {
		t.Fatalf("healthy candidate failed because another crashed: %v", err)
	}
	if escapes.Load() != 0 {
		t.Fatalf("a crash is not an escape; hook called %d times", escapes.Load())
	}
	requireGone(t, marker)
}

// A descendant that leaves its process group but stays in the candidate's
// session is attributed to that candidate only: it fails and the worker is
// told to exit, while a concurrent healthy candidate still succeeds.
func TestWorkerEngineAttributedEscapeFailsOnlyItsCandidate(t *testing.T) {
	for _, order := range []string{"healthy-after", "together"} {
		t.Run(order, func(t *testing.T) {
			marker := sleepMarker(t)
			bad := fakeCLI(t, "")
			bad = fakeCLI2(t, bad, leaveGroupScript(t, &bad, marker))
			good := fakeCLI(t, "")
			gate := gateDir(t, &good)
			body := `sleep ` + marker + ` </dev/null >/dev/null 2>&1 &
cp "$src" "$dst"`
			if order == "healthy-after" {
				body = gatedScript(gate, body)
			}
			good = fakeCLI2(t, good, body)
			good.Scratch = bad.Scratch
			var escapes atomic.Int32
			bad.Escaped = func() { escapes.Add(1) }
			var goodEscapes atomic.Int32
			good.Escaped = func() { goodEscapes.Add(1) }
			for range 5 {
				goodDone := make(chan error, 1)
				go func() { _, err := importBlank(t, t.Context(), good); goodDone <- err }()
				_, err := importBlank(t, t.Context(), bad)
				if !errors.Is(err, ErrEngineEscaped) {
					t.Fatalf("escaping candidate must fail closed, got %v", err)
				}
				openGate(t, gate)
				if err := <-goodDone; err != nil {
					t.Fatalf("healthy candidate failed for another candidate's escape: %v", err)
				}
				_ = os.Remove(gate)
			}
			if escapes.Load() != 5 || goodEscapes.Load() != 0 {
				t.Fatalf("escape hook: bad %d (want 5), healthy %d (want 0)", escapes.Load(), goodEscapes.Load())
			}
			requireGone(t, marker)
		})
	}
}

// The engine cannot start a session, become a subreaper, or create
// namespaces, so no engine descendant can hold a session ID that is not its
// candidate's.
func TestWorkerEngineCannotStartSessionsSubreapersOrNamespaces(t *testing.T) {
	engine := fakeCLI(t, "")
	engine = fakeCLI2(t, engine, sandboxProbeScript(t, &engine))
	var escapes atomic.Int32
	engine.Escaped = func() { escapes.Add(1) }
	if _, err := importBlank(t, t.Context(), engine); err != nil {
		t.Fatalf("engine process-control call was not refused: %v", err)
	}
	if escapes.Load() != 0 {
		t.Fatalf("probe is not an escape; hook called %d times", escapes.Load())
	}
}

// Backstop: a worker child in no registered session cannot be attributed, so
// the sweep kills it and fails every candidate in flight closed.
func TestSweepFailsEveryCandidateInFlightOnUnattributedProcess(t *testing.T) {
	if err := PrepareEngineSandbox(); err != nil {
		t.Skipf("engine sandbox unavailable: %v", err)
	}
	marker := sleepMarker(t)
	stray := exec.Command("/usr/bin/sleep", marker)
	stray.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := stray.Start(); err != nil {
		t.Fatal(err)
	}
	// Session IDs that no process can have, standing in for two candidates.
	sweeping, other := &engineRun{session: -2}, &engineRun{session: -3}
	engineRuns.Lock()
	engineRuns.live[sweeping.session], engineRuns.live[other.session] = sweeping, other
	engineRuns.Unlock()
	defer func() {
		engineRuns.Lock()
		delete(engineRuns.live, sweeping.session)
		delete(engineRuns.live, other.session)
		engineRuns.Unlock()
	}()
	if err := sweepEngineSurvivors(sweeping); err != nil {
		t.Fatal(err)
	}
	engineRuns.Lock()
	failed := sweeping.escaped && other.escaped
	engineRuns.Unlock()
	if !failed {
		t.Fatal("unattributed process must fail every candidate in flight")
	}
	if err := stray.Wait(); err == nil {
		t.Fatal("unattributed process was not killed")
	}
	requireGone(t, marker)
}

// Each engine process runs with resource limits and is the first OOM victim,
// so one candidate cannot take the worker or its siblings down with it.
func TestWorkerEngineRunsWithResourceLimitsAndOOMPreference(t *testing.T) {
	marker := sleepMarker(t)
	engine := fakeCLI(t, `if head -c 3145728 /dev/zero > "$TMPDIR/big" 2>/dev/null; then exit 76; fi
rm -f "$TMPDIR/big"
sleep `+marker+` </dev/null >/dev/null 2>&1
cp "$src" "$dst"`)
	engine.Limits = EngineLimits{AddressSpace: 512 << 20, FileSize: 2 << 20, CPUSeconds: 7}
	done := make(chan error, 1)
	go func() { _, err := importBlank(t, t.Context(), engine); done <- err }()
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 {
		if pids := processesWithArg(marker); len(pids) == 1 {
			pid = pids[0]
		} else if time.Now().After(deadline) {
			t.Fatal("engine did not start")
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	adj, _ := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", pid))
	limits, _ := os.ReadFile(fmt.Sprintf("/proc/%d/limits", pid))
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if err := <-done; err != nil {
		t.Fatalf("limited engine failed (file size limit not enforced?): %v", err)
	}
	if strings.TrimSpace(string(adj)) != "1000" {
		t.Fatalf("engine oom_score_adj %q, want 1000", adj)
	}
	for _, want := range []string{
		`Max cpu time\s+7\s+7\s`,
		`Max file size\s+2097152\s+2097152\s`,
		`Max address space\s+536870912\s+536870912\s`,
	} {
		if !regexp.MustCompile(want).Match(limits) {
			t.Fatalf("engine limits lack %s:\n%s", want, limits)
		}
	}
}

// The engine owns its candidate directory and can remove its own permissions
// (Landlock does not restrict chmod). Cleanup restores them without following
// links, so the worker does not lose the directory or become unavailable.
func TestWorkerEngineCleanupSurvivesUnreadableDirectories(t *testing.T) {
	for _, scenario := range []string{"nested", "candidate-dir"} {
		t.Run(scenario, func(t *testing.T) {
			body := `mkdir -p "$TMPDIR/d/e" && touch "$TMPDIR/d/e/f" && chmod 000 "$TMPDIR/d/e" "$TMPDIR/d"
cp "$src" "$dst"`
			if scenario == "candidate-dir" {
				body = `cp "$src" "$dst"; chmod 000 "$TMPDIR"`
			}
			engine := fakeCLI(t, body)
			var escapes atomic.Int32
			engine.Escaped = func() { escapes.Add(1) }
			_, err := importBlank(t, t.Context(), engine)
			if errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("permission change made the worker unavailable: %v", err)
			}
			if scenario == "nested" && err != nil {
				t.Fatal(err)
			}
			if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*")); len(files) != 0 {
				t.Fatalf("scratch survived: %v", files)
			}
			if escapes.Load() != 0 {
				t.Fatalf("hook called %d times", escapes.Load())
			}
		})
	}
}

func TestMakeRemovableDoesNotFollowLinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.Chmod(outside, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(outside, 0700)
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0); err != nil {
		t.Fatal(err)
	}
	if err := makeRemovable(dir); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(outside); err != nil || st.Mode().Perm() != 0500 {
		t.Fatalf("link target changed: %v %v", st.Mode(), err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

// Capacity N admits N concurrent candidates; candidate N+1 is refused
// before any scratch or engine process exists.
func TestWorkerHandlerCapacityNRunsConcurrentlyAndRefusesNPlusOne(t *testing.T) {
	const capacity = 3
	engine := fakeCLI(t, "")
	gate := gateDir(t, &engine)
	engine = fakeCLI2(t, engine, gatedScript(gate, `cp "$src" "$dst"`))
	secret := strings.Repeat("c", 64)
	handler, err := NewWorkerHandler(secret, engine, capacity)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"snapshot":` + string(blankWorkbook(t)) + `,"command":{"kind":"import"}}`
	request := func() int {
		req := httptest.NewRequest(http.MethodPost, "/v1/candidate", strings.NewReader(body))
		req.Header.Set(workerServiceHeader, secret)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	codes := make(chan int, capacity)
	for range capacity {
		go func() { codes <- request() }()
	}
	scratch := func() int {
		dirs, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*"))
		return len(dirs)
	}
	deadline := time.Now().Add(5 * time.Second)
	for scratch() != capacity {
		if time.Now().After(deadline) {
			openGate(t, gate)
			t.Fatalf("%d of %d candidates started", scratch(), capacity)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code := request(); code != http.StatusTooManyRequests {
		openGate(t, gate)
		t.Fatalf("candidate past capacity: status %d, want 429", code)
	}
	if n := scratch(); n != capacity {
		openGate(t, gate)
		t.Fatalf("refused candidate left hidden work: %d candidate dirs", n)
	}
	openGate(t, gate)
	for range capacity {
		if code := <-codes; code != http.StatusOK {
			t.Fatalf("admitted candidate status %d", code)
		}
	}
	if code := request(); code != http.StatusOK {
		t.Fatalf("capacity not returned: %d", code)
	}
}

// The worker reads engine output without following links, and only from a
// regular file with a single link, so a weakened sandbox cannot turn the
// output path into a read of a worker-private file.
func TestReadBoundedRejectsLinksAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "service-token")
	if err := os.WriteFile(private, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink, hardlink, fifo, regular := filepath.Join(dir, "sym.json"), filepath.Join(dir, "hard.json"), filepath.Join(dir, "fifo.json"), filepath.Join(dir, "ok.json")
	if err := os.Symlink(private, symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(private, hardlink); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regular, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{symlink, hardlink, fifo, dir} {
		if b, err := readBounded(path); err == nil {
			t.Fatalf("%s read as engine output: %q", filepath.Base(path), b)
		}
	}
	if b, err := readBounded(regular); err != nil || string(b) != `{}` {
		t.Fatalf("regular output: %q %v", b, err)
	}
}

// fakeCLI2 rewrites an engine's fake CLI body while keeping its scratch and
// read-only roots, so a test can add roots before choosing the script.
func fakeCLI2(t *testing.T, engine WorkerEngine, body string) WorkerEngine {
	t.Helper()
	script := "#!/bin/sh\nset -u\nsrc=\"$3\"\ndst=\"$5\"\n" + body + "\n"
	if err := os.WriteFile(engine.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	engine.digest = hex.EncodeToString(sum[:])
	return engine
}

// The pinned CLI runs concurrently on different workbooks under the sandbox,
// each candidate in its own scratch, with no survivor and no escape.
func TestRealGridCraftConcurrentCandidatesOnDifferentWorkbooks(t *testing.T) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		t.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY to the pinned CLI to execute real engine concurrency")
	}
	if err := PrepareEngineSandbox(); err != nil {
		t.Fatalf("engine sandbox unavailable: %v", err)
	}
	var escapes atomic.Int32
	engine := WorkerEngine{Binary: binary, Scratch: t.TempDir(), Escaped: func() { escapes.Add(1) }}
	raw := blankWorkbook(t)
	const parallel = 4
	errs := make(chan error, parallel)
	for i := range parallel {
		go func() {
			base, err := engine.Run(t.Context(), raw, Command{Kind: "import", Snapshot: raw})
			if err != nil {
				errs <- fmt.Errorf("import %d: %w", i, err)
				return
			}
			input := fmt.Sprint(100 + i)
			edited, err := engine.Run(t.Context(), base.Result, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: input})
			if err != nil {
				errs <- fmt.Errorf("edit %d: %w", i, err)
				return
			}
			if !strings.Contains(string(edited.Result), input) {
				errs <- fmt.Errorf("edit %d: result lacks its own value", i)
				return
			}
			errs <- nil
		}()
	}
	for range parallel {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if escapes.Load() != 0 {
		t.Fatalf("real engine reported %d escapes", escapes.Load())
	}
	if files, _ := filepath.Glob(filepath.Join(engine.Scratch, "workbook-*")); len(files) != 0 {
		t.Fatalf("scratch survived: %v", files)
	}
}

// The worker hashes the pinned CLI once at startup. Each candidate then only
// checks that the file is still the one that was hashed; a changed binary is
// refused before any engine process starts.
func TestWorkerEngineVerifiesDigestOnceAndRefusesChangedBinary(t *testing.T) {
	engine := fakeCLI(t, `cp "$src" "$dst"`)
	wrong := engine
	wrong.digest = strings.Repeat("0", 64)
	if err := wrong.Verify(); !errors.Is(err, ErrCandidate) {
		t.Fatalf("wrong digest must refuse to serve, got %v", err)
	}
	if err := engine.Verify(); err != nil {
		t.Fatal(err)
	}
	var exits atomic.Int32
	engine.Escaped = func() { exits.Add(1) }
	if engine.pinned == nil {
		t.Fatal("verified engine has no recorded identity")
	}
	if _, err := importBlank(t, t.Context(), engine); err != nil {
		t.Fatalf("verified engine: %v", err)
	}
	// Same content, new file: the recorded identity no longer matches, and a
	// changed identity is never re-trusted without a restart.
	marker := filepath.Join(filepath.Dir(engine.Binary), "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	replacement := engine.Binary + ".new"
	if err := os.WriteFile(replacement, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, engine.Binary); err != nil {
		t.Fatal(err)
	}
	if _, err := importBlank(t, t.Context(), engine); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("replaced engine binary must fail closed, got %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replaced engine binary was executed")
	}
	if exits.Load() != 1 {
		t.Fatalf("changed binary must make the worker exit for a re-verifying restart; hook called %d times", exits.Load())
	}
}

// BenchmarkRealWorkerEngineCellSet measures one cell.set candidate on the
// pinned CLI with the digest checked per candidate or once at startup.
func BenchmarkRealWorkerEngineCellSet(b *testing.B) {
	binary := os.Getenv("CREWSHIP_TEST_GRIDCRAFT_BINARY")
	if binary == "" {
		b.Skip("set CREWSHIP_TEST_GRIDCRAFT_BINARY to the pinned CLI")
	}
	if err := PrepareEngineSandbox(); err != nil {
		b.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/blank-workbook.json")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("digest-only", func(b *testing.B) {
		engine := WorkerEngine{Binary: binary}
		for b.Loop() {
			if _, err := engine.hashBinary(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("identity-check-only", func(b *testing.B) {
		engine := WorkerEngine{Binary: binary}
		if err := engine.Verify(); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			if err := engine.pinned.check(binary); err != nil {
				b.Fatal(err)
			}
		}
	})
	for _, mode := range []string{"hash-per-candidate", "verified-once"} {
		b.Run(mode, func(b *testing.B) {
			engine := WorkerEngine{Binary: binary, Scratch: b.TempDir()}
			if mode == "verified-once" {
				if err := engine.Verify(); err != nil {
					b.Fatal(err)
				}
			}
			command := Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"}
			for b.Loop() {
				if _, err := engine.Run(b.Context(), raw, command); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// The worker sets the engine's limits while it is stopped at exec, so the Go
// launcher never runs under them. Before that, a launcher lowering its own
// address space limit crashed now and then with "cannot allocate memory",
// mostly in race builds. Run many launcher executions to catch a regression.
func TestWorkerEngineLauncherNeverRunsUnderEngineLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("long launcher loop")
	}
	engine := fakeCLI(t, `cp "$src" "$dst"`)
	runs := 500
	if os.Getenv("CREWSHIP_TEST_LAUNCHER_RUNS") != "" {
		if _, err := fmt.Sscanf(os.Getenv("CREWSHIP_TEST_LAUNCHER_RUNS"), "%d", &runs); err != nil {
			t.Fatal(err)
		}
	}
	const parallel = 4
	var failures atomic.Int32
	var first atomic.Value
	var wg sync.WaitGroup
	for w := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < runs; i += parallel {
				if _, err := importBlank(t, t.Context(), engine); err != nil {
					failures.Add(1)
					first.CompareAndSwap(nil, err.Error())
				}
			}
		}()
	}
	wg.Wait()
	if n := failures.Load(); n != 0 {
		t.Fatalf("%d of %d launcher executions failed; first: %v", n, runs, first.Load())
	}
}
