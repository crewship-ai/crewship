package craftengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RuntimeProcess names an engine that runs in a separate worker process.
const RuntimeProcess = "wasm-process"

// Process is an Engine whose every Run is one engine command in its own worker
// process (WorkerMain). The in-process WASM engine has to poll its context, which
// made a real 235 000 cell workbook 4.6 times slower to open (122 s against 26 s)
// and so too slow for the 60 s deadline of a save or an agent run; a worker
// process is stopped by killing it, which also ends a guest stuck in a loop, and
// costs nothing while it runs. The guest is the same module, with no network and
// one working directory.
type Process struct {
	application string
	sha         string
	exe         string
	exeArgs     []string
	load        func() ([]byte, error)
	limits      Limits
	tempRoot    string
	env         []string

	once     sync.Once
	dir      string
	path     string
	initErr  error
	slots    chan struct{}
	closeOne sync.Once
}

// NewProcess prepares the engine. exe and exeArgs re-execute the program that
// calls WorkerMain (the server binary and its worker flag). The module bytes
// come from load on the first run, must hash to sha256Hex and are written to a
// private file once.
func NewProcess(application, sha256Hex, exe string, exeArgs []string, load func() ([]byte, error), limits Limits) (*Process, error) {
	if load == nil || len(sha256Hex) != 64 || exe == "" {
		return nil, ErrUnavailable
	}
	limits = limits.resolved()
	return &Process{application: application, sha: sha256Hex, exe: exe, exeArgs: append([]string(nil), exeArgs...), load: load, limits: limits, slots: make(chan struct{}, limits.Concurrency)}, nil
}

// WithEnv adds environment entries to the worker (tests; production passes none).
func (p *Process) WithEnv(entries ...string) *Process {
	p.env = append(p.env, entries...)
	return p
}

func (p *Process) Application() string { return p.application }
func (p *Process) Runtime() string     { return RuntimeProcess }
func (p *Process) SHA256() string      { return p.sha }
func (p *Process) Root(string) string  { return GuestRoot }

func (p *Process) materialize() error {
	p.once.Do(func() {
		module, err := p.load()
		if err != nil {
			p.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if sum := sha256.Sum256(module); hex.EncodeToString(sum[:]) != p.sha {
			p.initErr = fmt.Errorf("%w: module digest mismatch", ErrUnavailable)
			return
		}
		dir, err := os.MkdirTemp(p.tempRoot, "crewship-craft-engine-")
		if err != nil {
			p.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		path := filepath.Join(dir, "engine.wasm")
		if err = os.WriteFile(path, module, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			p.initErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		p.dir, p.path = dir, path
		p.load = nil
	})
	return p.initErr
}

// killWriter stops the process the moment its output is refused (a limit), so
// a guest that keeps printing cannot sit blocked on a full pipe until the deadline.
type killWriter struct {
	w    io.Writer
	kill func()
}

func (k *killWriter) Write(b []byte) (int, error) {
	n, err := k.w.Write(b)
	if err != nil {
		k.kill()
	}
	return n, err
}

// Run executes one engine command (args as for the WASM engine) and returns
// when the process ends or ctx or the run limit is reached, in which case the
// process is killed. Output written to stdout and stderr is the engine's own.
func (p *Process) Run(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	if err := p.materialize(); err != nil {
		return err
	}
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
	}
	defer func() { <-p.slots }()
	ctx, cancel := context.WithTimeout(ctx, p.limits.Timeout)
	defer cancel()
	full := append(append([]string{}, p.exeArgs...), "--app", p.application, "--module", p.path, "--sha", p.sha, "--dir", dir, "--mem", strconv.Itoa(p.limits.MemoryMiB))
	full = append(append(full, workerCacheArgs(p.sha, p.path)...), "--")
	full = append(full, args...)
	cmd := exec.Command(p.exe, full...)
	cmd.Env = append([]string{"LANG=C.UTF-8"}, p.env...)
	cmd.Dir = dir
	setWorkerProcAttr(cmd)
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			if cmd.Process != nil {
				killWorkerProcess(cmd)
			}
		})
	}
	cmd.Stdout = &killWriter{stdout, kill}
	cmd.Stderr = &killWriter{stderr, kill}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return ErrFailed
		}
		return nil
	case <-ctx.Done():
		kill()
		<-done
		return fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
	}
}

// Close removes the private module file.
func (p *Process) Close(context.Context) error {
	p.closeOne.Do(func() {
		if p.dir != "" {
			_ = os.RemoveAll(p.dir)
		}
	})
	return nil
}

// ProbeProcess reports whether exe, started with exeArgs and ProbeFlag, answers as an engine
// worker. A binary that does not (a test binary, an older build) answers something else and
// the in-process engine is used instead.
func ProbeProcess(ctx context.Context, exe string, exeArgs []string, env ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, append(append([]string{}, exeArgs...), ProbeFlag)...)
	cmd.Env = append([]string{"LANG=C.UTF-8"}, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || strings.TrimSpace(out.String()) != ProbeAnswer {
		return ErrUnavailable
	}
	return nil
}
