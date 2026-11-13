package craftengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// The tests assemble tiny WASI modules byte by byte, so no compiled binary is
// committed. Each module imports fd_write and proc_exit, exports its memory and
// _start, and holds an iovec at 0 that points at "hello\n" at 16.

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func section(id byte, body ...[]byte) []byte {
	content := bytes.Join(body, nil)
	return append(append([]byte{id}, uleb(uint32(len(content)))...), content...)
}

func name(s string) []byte { return append(uleb(uint32(len(s))), s...) }

// wasiModule returns a module whose _start runs code; memoryPages is its
// declared minimum memory.
func wasiModule(code []byte, memoryPages uint32) []byte {
	types := section(1, []byte{3},
		[]byte{0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f}, // fd_write
		[]byte{0x60, 1, 0x7f, 0},                         // proc_exit
		[]byte{0x60, 0, 0})                               // _start
	imports := section(2, []byte{2},
		name("wasi_snapshot_preview1"), name("fd_write"), []byte{0, 0},
		name("wasi_snapshot_preview1"), name("proc_exit"), []byte{0, 1})
	funcs := section(3, []byte{1, 2})
	memory := section(5, []byte{1, 0}, uleb(memoryPages))
	exports := section(7, []byte{2}, name("memory"), []byte{2, 0}, name("_start"), []byte{0, 2})
	body := append([]byte{0}, append(code, 0x0b)...) // no locals
	codeSec := section(10, []byte{1}, uleb(uint32(len(body))), body)
	data := append([]byte{16, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, "hello\n"...)
	dataSec := section(11, []byte{1, 0, 0x41, 0, 0x0b}, uleb(uint32(len(data))), data)
	return bytes.Join([][]byte{{0, 'a', 's', 'm', 1, 0, 0, 0}, types, imports, funcs, memory, exports, codeSec, dataSec}, nil)
}

var (
	// fd_write(1, iovec 0, 1 iovec, nwritten at 8); drop
	writeHello = []byte{0x41, 1, 0x41, 0, 0x41, 1, 0x41, 8, 0x10, 0, 0x1a}
	exitThree  = []byte{0x41, 3, 0x10, 1}
	exitZero   = append(append([]byte{}, writeHello...), 0x41, 0, 0x10, 1)
	spin       = []byte{0x03, 0x40, 0x0c, 0, 0x0b}
)

func newTestWASM(t *testing.T, module []byte, limits Limits) *WASM {
	t.Helper()
	e, err := NewWASM("wordcraft", module, "", limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e
}

func TestWASMRunOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		code   []byte
		stdout string
		fails  bool
	}{
		{"returns from _start", writeHello, "hello\n", false},
		{"exits with zero", exitZero, "hello\n", false},
		{"exits with non-zero", exitThree, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestWASM(t, wasiModule(c.code, 1), Limits{})
			var stdout, stderr bytes.Buffer
			err := e.Run(context.Background(), t.TempDir(), []string{"convert"}, &stdout, &stderr)
			if c.fails != (err != nil) || (err != nil && !errors.Is(err, ErrFailed)) {
				t.Fatalf("err = %v", err)
			}
			if stdout.String() != c.stdout {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestWASMRunsAreIsolatedAndRepeatable(t *testing.T) {
	e := newTestWASM(t, wasiModule(writeHello, 1), Limits{Concurrency: 1})
	for i := 0; i < 3; i++ {
		var stdout bytes.Buffer
		if err := e.Run(context.Background(), t.TempDir(), nil, &stdout, &bytes.Buffer{}); err != nil || stdout.String() != "hello\n" {
			t.Fatalf("run %d: %q %v", i, stdout.String(), err)
		}
	}
}

func TestWASMTimeoutStopsARunawayModule(t *testing.T) {
	e := newTestWASM(t, wasiModule(spin, 1), Limits{Timeout: 50 * time.Millisecond})
	started := time.Now()
	err := e.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, ErrFailed) || time.Since(started) > 5*time.Second {
		t.Fatalf("runaway module: %v after %s", err, time.Since(started))
	}
}

func TestWASMMemoryLimit(t *testing.T) {
	// 32 pages = 2 MiB declared; a 1 MiB limit refuses it.
	e := newTestWASM(t, wasiModule(writeHello, 32), Limits{MemoryMiB: 1})
	if err := e.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("module over its memory limit ran")
	}
}

func TestWASMRefusesWrongDigestAndInvalidModule(t *testing.T) {
	module := wasiModule(writeHello, 1)
	sum := sha256.Sum256(module)
	if _, err := NewWASM("wordcraft", module, hex.EncodeToString(sum[:]), Limits{}); err != nil {
		t.Fatalf("matching digest refused: %v", err)
	}
	if _, err := NewWASM("wordcraft", module, string(bytes.Repeat([]byte("0"), 64)), Limits{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("wrong digest: %v", err)
	}
	if _, err := NewWASM("wordcraft", nil, "", Limits{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty module: %v", err)
	}
	broken := newTestWASM(t, []byte("\x00asm\x01\x00\x00\x00garbage"), Limits{})
	if err := broken.Prepare(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid module prepared: %v", err)
	}
}

func TestWASMCancelledContextDoesNotWaitForASlot(t *testing.T) {
	e := newTestWASM(t, wasiModule(spin, 1), Limits{Concurrency: 1, Timeout: time.Second})
	done := make(chan struct{})
	go func() {
		_ = e.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.Run(ctx, t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, ErrFailed) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	<-done
}

func TestLazyWASMVerifiesBytesWhenItCompiles(t *testing.T) {
	module := wasiModule(writeHello, 1)
	sum := sha256.Sum256(module)
	loads := 0
	e, err := NewLazyWASM("gridcraft", hex.EncodeToString(sum[:]), func() ([]byte, error) { loads++; return module, nil }, Limits{})
	if err != nil || loads != 0 {
		t.Fatalf("lazy engine loaded early: %d %v", loads, err)
	}
	defer e.Close(context.Background())
	for i := 0; i < 2; i++ {
		if err := e.Run(context.Background(), t.TempDir(), nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 1 {
		t.Fatalf("module loaded %d times", loads)
	}
	wrong, _ := NewLazyWASM("gridcraft", string(bytes.Repeat([]byte("1"), 64)), func() ([]byte, error) { return module, nil }, Limits{})
	if err := wrong.Prepare(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tampered lazy module: %v", err)
	}
}

func TestLimitsResolved(t *testing.T) {
	got := Limits{MemoryMiB: 9000}.resolved()
	if got.MemoryMiB != 4096 || got.Timeout != 60*time.Second || got.Concurrency != 2 {
		t.Fatalf("resolved = %+v", got)
	}
}
