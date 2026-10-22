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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary act as the engine sandbox launcher, exactly as
// the worker binary does in its image.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == EngineSandboxFlag {
		err := ExecEngineSandbox(os.Args[2:])
		fmt.Fprintln(os.Stderr, "engine sandbox:", err)
		os.Exit(126)
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

// leaveGroupScript starts a descendant that leaves the engine's process group
// but stays in its session, and waits until it has done so.
func leaveGroupScript(t *testing.T, engine *WorkerEngine, marker string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	engine.ReadOnly = append(engine.ReadOnly, filepath.Dir(self))
	return fmt.Sprintf(`%q %s %s "$TMPDIR/left" </dev/null >/dev/null 2>&1 &
while [ ! -e "$TMPDIR/left" ]; do sleep 0.01; done
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

// detachScript double-forks a child that leaves the engine's session and
// process group, and waits until it has done so before reporting success.
func detachScript(marker string) string {
	return `( setsid sh -c 'echo > "$1"; exec sleep ` + marker + `' sh "$TMPDIR/escaped" </dev/null >/dev/null 2>&1 & )
while [ ! -e "$TMPDIR/escaped" ]; do sleep 0.01; done
cp "$src" "$dst"`
}

func TestWorkerEngineDetachedChildFailsClosedAndIsKilled(t *testing.T) {
	marker := sleepMarker(t)
	// Double fork plus setsid: the child leaves the engine's process group and
	// session and is reparented when its parent exits; the CLI reports success.
	engine := fakeCLI(t, detachScript(marker))
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
	engine := fakeCLI(t, detachScript(marker))
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

// A descendant that starts its own session cannot be attributed to a
// candidate. The worker then fails every candidate in flight closed (it is
// about to exit anyway) rather than return output an escaped process could
// have influenced.
func TestWorkerEngineUnattributedEscapeFailsEveryCandidateInFlight(t *testing.T) {
	marker := sleepMarker(t)
	bad := fakeCLI(t, detachScript(marker))
	good := fakeCLI(t, "")
	gate := gateDir(t, &good)
	good = fakeCLI2(t, good, gatedScript(gate, `cp "$src" "$dst"`))
	var escapes atomic.Int32
	bad.Escaped = func() { escapes.Add(1) }
	good.Escaped = bad.Escaped
	goodDone := make(chan error, 1)
	go func() { _, err := importBlank(t, t.Context(), good); goodDone <- err }()
	// Let the healthy candidate start before the escape is detected.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if dirs, _ := filepath.Glob(filepath.Join(good.Scratch, "workbook-*")); len(dirs) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("healthy candidate did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := importBlank(t, t.Context(), bad); !errors.Is(err, ErrEngineEscaped) {
		t.Fatalf("escaping candidate must fail closed, got %v", err)
	}
	openGate(t, gate)
	if err := <-goodDone; !errors.Is(err, ErrEngineEscaped) {
		t.Fatalf("candidate in flight during an unattributed escape must fail closed, got %v", err)
	}
	if escapes.Load() == 0 {
		t.Fatal("worker exit hook not called")
	}
	requireGone(t, marker)
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
	if !engine.verifiedIdentity() {
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
