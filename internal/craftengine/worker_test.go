package craftengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The worker tests re-execute the test binary as the worker process, so the
// real process boundary, pipes and kill are exercised without a compiled engine.
func TestMain(m *testing.M) {
	if os.Getenv("CRAFTENGINE_TEST_SELFKILL") == "1" {
		killSelf()
	}
	if os.Getenv("CRAFTENGINE_TEST_WORKER") == "1" {
		os.Exit(WorkerMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// wasiPrinting returns a module that writes msg to stdout and exits 0.
func wasiPrinting(msg string) []byte { return wasiPrintingExit(msg, 0) }

// wasiPrintingExit writes msg to stdout and exits with code (0..63).
func wasiPrintingExit(msg string, exitCode byte) []byte {
	return wasiPrintingProgram(msg, exitCode, false)
}

func wasiPrintingProgram(msg string, exitCode byte, waitForInput bool) []byte {
	types := section(1, []byte{3},
		[]byte{0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f},
		[]byte{0x60, 1, 0x7f, 0},
		[]byte{0x60, 0, 0})
	imports := section(2, []byte{3},
		name("wasi_snapshot_preview1"), name("fd_write"), []byte{0, 0},
		name("wasi_snapshot_preview1"), name("proc_exit"), []byte{0, 1},
		name("wasi_snapshot_preview1"), name("fd_read"), []byte{0, 0})
	funcs := section(3, []byte{1, 2})
	memory := section(5, []byte{1, 0}, uleb(1))
	exports := section(7, []byte{2}, name("memory"), []byte{2, 0}, name("_start"), []byte{0, 3})
	var code []byte
	if waitForInput {
		// fd_read(0, iovec at 8, 1, bytes read at 256) blocks for the request.
		code = append(code, 0x41, 0, 0x41, 8, 0x41, 1, 0x41, 0x80, 0x02, 0x10, 2, 0x1a)
	}
	code = append(code, writeHello...)
	code = append(code, 0x41, exitCode, 0x10, 1)
	body := append([]byte{0}, append(code, 0x0b)...)
	codeSec := section(10, []byte{1}, uleb(uint32(len(body))), body)
	iov := append([]byte{16, 0, 0, 0}, byte(len(msg)), 0, 0, 0)
	data := append(append(iov, 128, 0, 0, 0, 1, 0, 0, 0), msg...) // byte buffer at 128, length 1
	dataSec := section(11, []byte{1, 0, 0x41, 0, 0x0b}, uleb(uint32(len(data))), data)
	return bytes.Join([][]byte{{0, 'a', 's', 'm', 1, 0, 0, 0}, types, imports, funcs, memory, exports, codeSec, dataSec}, nil)
}

func startTestWorker(t *testing.T, module []byte, sha string) (*Worker, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.wasm")
	if err := os.WriteFile(path, module, 0o600); err != nil {
		t.Fatal(err)
	}
	if sha == "" {
		sum := sha256.Sum256(module)
		sha = hex.EncodeToString(sum[:])
	}
	return StartWorker(WorkerSpec{
		Exe: os.Args[0], Env: []string{"CRAFTENGINE_TEST_WORKER=1"},
		Application: "gridcraft", ModulePath: path, SHA256: sha, Dir: dir,
		EngineArgs: []string{"mcp"},
	})
}

func TestWorkerAnswersOneRequestOverStdio(t *testing.T) {
	w, err := startTestWorker(t, wasiPrintingProgram(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`+"\n", 0, true), "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := w.Call(ctx, "ping", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("call: %s %v, worker stderr: %s", res, err, w.Stderr())
	}
}

func TestWorkerDrainsRepliesAfterProcessExit(t *testing.T) {
	w := &Worker{lines: make(chan []byte, 3), done: make(chan struct{})}
	close(w.done)
	w.readLines(strings.NewReader("first\nsecond\nfinal reply\n"), 256)
	for _, want := range []string{"first", "second", "final reply"} {
		select {
		case got := <-w.lines:
			if string(got) != want {
				t.Fatalf("buffered reply %q, want %q", got, want)
			}
		default:
			t.Fatalf("missing buffered reply %q", want)
		}
	}
}

// A run that never answers must be killed hard when its deadline passes, even
// though the engine inside runs without close-on-context-done checks.
func TestWorkerIsKilledWhenItMissesTheDeadline(t *testing.T) {
	w, err := startTestWorker(t, wasiModule(spin, 1), "")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = w.Call(ctx, "ping", nil)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-w.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("worker process still alive after the deadline")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatalf("took %s", time.Since(started))
	}
	if _, err = w.Call(context.Background(), "ping", nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("a dead worker answered: %v", err)
	}
}

func TestWorkerRefusesAModuleWithTheWrongDigest(t *testing.T) {
	w, err := startTestWorker(t, wasiModule(spin, 1), string(bytes.Repeat([]byte("0"), 64)))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err = w.Call(ctx, "ping", nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("tampered module answered: %v", err)
	}
	select {
	case <-w.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("worker kept running a module with the wrong digest")
	}
}

func TestWorkerCapsAResponseLine(t *testing.T) {
	long := `{"jsonrpc":"2.0","id":1,"result":"` + string(bytes.Repeat([]byte("x"), 200)) + `"}` + "\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.wasm")
	module := wasiPrinting(long[:120])
	_ = os.WriteFile(path, module, 0o600)
	sum := sha256.Sum256(module)
	w, err := StartWorker(WorkerSpec{Exe: os.Args[0], Env: []string{"CRAFTENGINE_TEST_WORKER=1"}, Application: "gridcraft", ModulePath: path,
		SHA256: hex.EncodeToString(sum[:]), Dir: dir, EngineArgs: []string{"mcp"}, MaxLineBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err = w.Call(ctx, "ping", nil); !errors.Is(err, ErrFailed) {
		t.Fatalf("oversized line: %v", err)
	}
}

func newTestProcess(t *testing.T, module []byte, limits Limits) *Process {
	t.Helper()
	sum := sha256.Sum256(module)
	p, err := NewProcess("gridcraft", hex.EncodeToString(sum[:]), os.Args[0], nil, func() ([]byte, error) { return module, nil }, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p.WithEnv("CRAFTENGINE_TEST_WORKER=1")
}

func TestProcessEngineRunsACommandAndReturnsItsOutput(t *testing.T) {
	p := newTestProcess(t, wasiPrinting("hello\n"), Limits{})
	var out, errb bytes.Buffer
	if err := p.Run(context.Background(), t.TempDir(), []string{"convert"}, &out, &errb); err != nil || out.String() != "hello\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	if p.Runtime() != RuntimeProcess || p.Root("x") != GuestRoot || p.Application() != "gridcraft" {
		t.Fatal("engine identity")
	}
}

func TestProcessEngineKillsARunawayGuestAtTheDeadline(t *testing.T) {
	p := newTestProcess(t, wasiModule(spin, 1), Limits{Timeout: 300 * time.Millisecond})
	started := time.Now()
	err := p.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, ErrFailed) || time.Since(started) > 5*time.Second {
		t.Fatalf("%v after %s", err, time.Since(started))
	}
}

func TestProcessEngineFailsOnANonZeroExitAndOnARefusedWrite(t *testing.T) {
	p := newTestProcess(t, wasiModule(exitThree, 1), Limits{})
	if err := p.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("exit 3: %v", err)
	}
	q := newTestProcess(t, wasiPrinting(strings.Repeat("x", 200)), Limits{})
	if err := q.Run(context.Background(), t.TempDir(), nil, &refusingWriter{}, &bytes.Buffer{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("refused output: %v", err)
	}
}

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("limit") }

func TestProcessEngineRefusesAModuleWithTheWrongDigestAndReportsUnavailable(t *testing.T) {
	module := wasiPrinting("hi")
	p, err := NewProcess("gridcraft", strings.Repeat("0", 64), os.Args[0], nil, func() ([]byte, error) { return module, nil }, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close(context.Background())
	p.WithEnv("CRAFTENGINE_TEST_WORKER=1")
	if err = p.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tampered module: %v", err)
	}
}

func TestProcessEngineSlotsAreReleasedAndACancelledWaiterDoesNotBlock(t *testing.T) {
	p := newTestProcess(t, wasiModule(spin, 1), Limits{Concurrency: 1, Timeout: 700 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		_ = p.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{})
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Run(ctx, t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	<-done
}

type stubEngine struct {
	err   error
	calls int
}

func (s *stubEngine) Application() string { return "gridcraft" }
func (s *stubEngine) Runtime() string     { return "stub" }
func (s *stubEngine) SHA256() string      { return strings.Repeat("a", 64) }
func (s *stubEngine) Root(string) string  { return GuestRoot }
func (s *stubEngine) Run(context.Context, string, []string, io.Writer, io.Writer) error {
	s.calls++
	return s.err
}

func TestFallbackUsesTheSecondEngineOnlyWhenTheFirstCannotRunAtAll(t *testing.T) {
	for name, c := range map[string]struct {
		first      error
		wantSecond int
		wantErrIs  error
	}{
		"unavailable falls back": {ErrUnavailable, 1, nil},
		"a failed run is final":  {ErrFailed, 0, ErrFailed},
		"success":                {nil, 0, nil},
	} {
		first, second := &stubEngine{err: c.first}, &stubEngine{}
		f, err := NewFallback(first, second, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = f.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard)
		if second.calls != c.wantSecond || (c.wantErrIs != nil && !errors.Is(err, c.wantErrIs)) || (c.wantErrIs == nil && err != nil) {
			t.Errorf("%s: second=%d err=%v", name, second.calls, err)
		}
	}
	if _, err := NewFallback(&stubEngine{}, nil, nil); err == nil {
		t.Error("nil secondary accepted")
	}
}

// A fallback is never quiet: it warns once, and Status says which runtime served
// the last run and why the worker was not used.
func TestFallbackIsVisibleInStatusAndWarnsOnce(t *testing.T) {
	first, second := &processStub{err: ErrUnavailable}, &stubEngine{}
	var warns []string
	f, _ := NewFallback(first, second, func(reason string) { warns = append(warns, reason) })
	if st := f.Status(); st.Runtime != RuntimeProcess || st.Degraded {
		t.Fatalf("before any run: %+v", st)
	}
	for i := 0; i < 3; i++ {
		if err := f.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	st := f.Status()
	if st.Runtime != "stub" || !st.Degraded || st.Reason == "" || st.FallbackRuns != 3 || f.Runtime() != "stub" {
		t.Fatalf("after fallback: %+v", st)
	}
	if len(warns) != 1 {
		t.Fatalf("warned %d times", len(warns))
	}
	// a worker that works is reported as such
	ok := &processStub{}
	g, _ := NewFallback(ok, &stubEngine{}, nil)
	_ = g.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard)
	if st := g.Status(); st.Runtime != RuntimeProcess || st.Degraded || st.FallbackRuns != 0 {
		t.Fatalf("worker run: %+v", st)
	}
	// degraded from the start: the in-process engine, said so, warned at first use
	var w2 []string
	d, _ := NewDegraded(&stubEngine{}, "no worker binary", func(r string) { w2 = append(w2, r) })
	_ = d.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard)
	if st := d.Status(); !st.Degraded || st.Reason != "no worker binary" || st.Runtime != "stub" || len(w2) != 1 || w2[0] != "no worker binary" {
		t.Fatalf("degraded: %+v %v", st, w2)
	}
}

type processStub struct {
	stubEngine
	err error
}

func (p *processStub) Runtime() string { return RuntimeProcess }
func (p *processStub) Run(context.Context, string, []string, io.Writer, io.Writer) error {
	return p.err
}

func TestProcessEngineSurvivesAKilledOrOverLimitWorker(t *testing.T) {
	p := newTestProcess(t, wasiPrinting("ok\n"), Limits{})
	// the worker is SIGKILLed mid-run (what an OOM killer does): the run fails, the next one works
	p.WithEnv("CRAFTENGINE_TEST_SELFKILL=1")
	if err := p.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard); !errors.Is(err, ErrFailed) {
		t.Fatalf("killed worker: %v", err)
	}
	p.env = []string{"CRAFTENGINE_TEST_WORKER=1"}
	var out bytes.Buffer
	if err := p.Run(context.Background(), t.TempDir(), nil, &out, io.Discard); err != nil || out.String() != "ok\n" {
		t.Fatalf("after the kill: %q %v", out.String(), err)
	}
	// a guest that wants more memory than its limit does not start; the server side stays up
	big := newTestProcess(t, wasiModule(writeHello, 32), Limits{MemoryMiB: 1})
	if err := big.Run(context.Background(), t.TempDir(), nil, io.Discard, io.Discard); !errors.Is(err, ErrFailed) {
		t.Fatalf("over the memory limit: %v", err)
	}
}
