package craftengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// GuestRoot is where a WASM engine sees its working directory.
const GuestRoot = "/work"

// Limits bound one WASM run.
type Limits struct {
	// MemoryMiB caps the module's linear memory (default 1024, max 4096).
	MemoryMiB int
	// Timeout caps one run (default 60 s).
	Timeout time.Duration
	// Concurrency caps simultaneous runs of this engine (default 2).
	Concurrency int
}

func (l Limits) resolved() Limits {
	if l.MemoryMiB <= 0 {
		l.MemoryMiB = 1024
	}
	if l.MemoryMiB > 4096 {
		l.MemoryMiB = 4096
	}
	if l.Timeout <= 0 {
		l.Timeout = 60 * time.Second
	}
	if l.Concurrency <= 0 {
		l.Concurrency = 2
	}
	return l
}

// WASM is an engine compiled to WASI and run in-process.
type WASM struct {
	application string
	load        func() ([]byte, error)
	sha         string
	limits      Limits

	once     sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	initErr  error
	slots    chan struct{}
}

// NewWASM prepares an engine from module bytes. The module is compiled on the
// first run, not here, so a server that never opens a document pays nothing.
// expectedSHA256, when not empty, must match the bytes.
func NewWASM(application string, module []byte, expectedSHA256 string, limits Limits) (*WASM, error) {
	if len(module) == 0 {
		return nil, ErrUnavailable
	}
	sum := sha256.Sum256(module)
	sha := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && expectedSHA256 != sha {
		return nil, fmt.Errorf("%w: module digest mismatch", ErrUnavailable)
	}
	return NewLazyWASM(application, sha, func() ([]byte, error) { return module, nil }, limits)
}

// NewLazyWASM prepares an engine whose module bytes are loaded only when it is
// first compiled; they must then hash to sha256Hex.
func NewLazyWASM(application, sha256Hex string, load func() ([]byte, error), limits Limits) (*WASM, error) {
	if load == nil || len(sha256Hex) != 64 {
		return nil, ErrUnavailable
	}
	limits = limits.resolved()
	return &WASM{application: application, load: load, sha: sha256Hex, limits: limits, slots: make(chan struct{}, limits.Concurrency)}, nil
}

func (e *WASM) Application() string { return e.application }
func (e *WASM) Runtime() string     { return RuntimeWASM }
func (e *WASM) SHA256() string      { return e.sha }
func (e *WASM) Root(string) string  { return GuestRoot }
func (e *WASM) Limits() Limits      { return e.limits }

// Prepare compiles the module now. Admin's self-test calls it so a broken
// module is reported before anybody opens a document.
func (e *WASM) Prepare(ctx context.Context) error {
	e.once.Do(func() {
		// Compilation must not be cancelled by one caller's request context.
		bg := context.WithoutCancel(ctx)
		cfg := wazero.NewRuntimeConfig().
			WithCloseOnContextDone(true).
			WithMemoryLimitPages(uint32(e.limits.MemoryMiB) * 16) // 64 KiB pages
		e.runtime = wazero.NewRuntimeWithConfig(bg, cfg)
		if _, err := wasi_snapshot_preview1.Instantiate(bg, e.runtime); err != nil {
			e.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		module, err := e.load()
		if err != nil {
			e.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if sum := sha256.Sum256(module); hex.EncodeToString(sum[:]) != e.sha {
			e.initErr = fmt.Errorf("%w: module digest mismatch", ErrUnavailable)
			return
		}
		compiled, err := e.runtime.CompileModule(bg, module)
		if err != nil {
			e.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		e.compiled = compiled
		e.load = nil // the compiled form is all that is needed now
	})
	return e.initErr
}

// Run instantiates a fresh module instance for one command. The instance sees
// only dir (at GuestRoot), a minimal environment, the real clocks and no
// network; it is discarded afterwards.
func (e *WASM) Run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	if err := e.Prepare(ctx); err != nil {
		return err
	}
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
	}
	defer func() { <-e.slots }()
	ctx, cancel := context.WithTimeout(ctx, e.limits.Timeout)
	defer cancel()
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(append([]string{e.application + "-cli"}, args...)...).
		WithFSConfig(wazero.NewFSConfig().WithDirMount(dir, GuestRoot)).
		WithEnv("LANG", "C.UTF-8").WithEnv("HOME", GuestRoot).WithEnv("TMPDIR", GuestRoot).
		WithStdout(stdout).WithStderr(stderr).
		WithSysWalltime().WithSysNanotime().WithRandSource(rand.Reader)
	mod, err := e.runtime.InstantiateModule(ctx, e.compiled, cfg)
	if mod != nil {
		_ = mod.Close(context.WithoutCancel(ctx))
	}
	if err == nil {
		return nil
	}
	if exit, ok := err.(*sys.ExitError); ok && exit.ExitCode() == 0 {
		return nil
	}
	return ErrFailed
}

// Close releases the compiled module.
func (e *WASM) Close(ctx context.Context) error {
	if e.runtime != nil {
		return e.runtime.Close(ctx)
	}
	return nil
}
