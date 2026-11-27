package craftengine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// WorkerSpec describes one engine worker process.
type WorkerSpec struct {
	// Exe is the program that runs WorkerMain (the server binary re-executed,
	// or the test binary). Args are what it needs to reach WorkerMain.
	Exe  string
	Args []string
	// Env is the complete environment of the worker; nothing is inherited.
	Env []string

	Application string
	ModulePath  string
	SHA256      string
	// Dir is the one directory the engine sees.
	Dir string
	// MemoryMiB caps the guest's linear memory (default 1024, max 4096).
	MemoryMiB int
	// MaxLineBytes caps one response line from the engine (default 16 MiB).
	// What the engine prints is untrusted input.
	MaxLineBytes int
	// EngineArgs are the arguments of the engine itself (for example "mcp").
	EngineArgs []string
}

// Worker is a running engine process spoken to in JSON-RPC lines.
type Worker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte
	done   chan struct{}
	stderr *tail

	mu      sync.Mutex // serializes calls
	nextID  int
	killMu  sync.Once
	waitErr error

	// readDone closes when stdout reached its end; last is the last line read (lastMu).
	readDone chan struct{}
	lastMu   sync.Mutex
	last     []byte
}

// RPCError is an error answer of the engine to one call. The worker is still
// usable afterwards.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("engine error %d: %s", e.Code, e.Message) }

// StartWorker starts the process. It returns before the engine has opened
// anything: the first Call waits for it.
func StartWorker(spec WorkerSpec) (*Worker, error) {
	if spec.Exe == "" || spec.Dir == "" || spec.ModulePath == "" || spec.Application == "" {
		return nil, ErrUnavailable
	}
	if spec.MaxLineBytes <= 0 {
		spec.MaxLineBytes = 16 << 20
	}
	args := append([]string{}, spec.Args...)
	args = append(args, "--app", spec.Application, "--module", spec.ModulePath, "--sha", spec.SHA256, "--dir", spec.Dir)
	args = append(args, workerCacheArgs(spec.SHA256, spec.ModulePath)...)
	if spec.MemoryMiB > 0 {
		args = append(args, "--mem", strconv.Itoa(spec.MemoryMiB))
	}
	args = append(args, "--")
	args = append(args, spec.EngineArgs...)
	cmd := exec.Command(spec.Exe, args...)
	cmd.Env = append([]string{}, spec.Env...)
	cmd.Dir = spec.Dir
	setWorkerProcAttr(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	// An own pipe, not StdoutPipe: cmd.Wait closes that one as the process exits, and a line
	// written just before the exit could be lost; this one is read to its end.
	out, outW, err := os.Pipe()
	if err != nil {
		return nil, ErrUnavailable
	}
	cmd.Stdout = outW
	w := &Worker{cmd: cmd, stdin: in, lines: make(chan []byte, 16), done: make(chan struct{}), stderr: &tail{max: 4 << 10}, readDone: make(chan struct{})}
	cmd.Stderr = w.stderr
	if err = cmd.Start(); err != nil {
		_ = out.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	_ = outW.Close() // the child holds its own copy
	go func() {
		defer close(w.readDone)
		defer close(w.lines)
		defer out.Close()
		w.readLines(out, spec.MaxLineBytes)
	}()
	go func() {
		w.waitErr = cmd.Wait()
		close(w.done)
	}()
	return w, nil
}

func (w *Worker) readLines(out io.Reader, max int) {
	r := bufio.NewReaderSize(out, 64<<10)
	var line []byte
	draining := false
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			w.Kill() // an oversized answer is hostile or broken: stop it
			return
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(line) > 0 { // a last line without its newline
				w.keepLast(line)
			}
			return
		}
		w.keepLast(line)
		if !draining {
			answer := bytes.TrimRight(append([]byte(nil), line...), "\r\n")
			// A final answer can reach the pipe reader after cmd.Wait returns.
			// Queue it when there is room, even if the process has exited.
			select {
			case w.lines <- answer:
			default:
				select {
				case w.lines <- answer:
				case <-w.done:
					// Bounded queue full with no more consumer. Keep reading for LastLine.
					draining = true
				}
			}
		}
		line = line[:0]
	}
}

// Call sends one JSON-RPC request and waits for its answer. When ctx ends
// first the process is killed (a worker that missed a deadline has no state
// worth keeping) and ErrFailed is returned; so it is when the process died.
func (w *Worker) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
		return nil, ErrFailed
	default:
	}
	w.nextID++
	id := w.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	wrote := make(chan error, 1)
	go func() { _, e := w.stdin.Write(append(raw, '\n')); wrote <- e }()
	select {
	case e := <-wrote:
		if e != nil {
			w.Kill()
			return nil, ErrFailed
		}
	case <-ctx.Done():
		w.Kill()
		return nil, fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
	}
	for {
		select {
		case line, ok := <-w.lines:
			if !ok {
				return nil, ErrFailed
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &msg) != nil {
				w.Kill()
				return nil, ErrFailed
			}
			if len(msg.ID) == 0 || string(msg.ID) == "null" {
				continue // a notification
			}
			if string(msg.ID) != strconv.Itoa(id) {
				continue // an answer to something already abandoned
			}
			if msg.Error != nil {
				return nil, &RPCError{Code: msg.Error.Code, Message: clip(msg.Error.Message, 200)}
			}
			return msg.Result, nil
		case <-ctx.Done():
			w.Kill()
			return nil, fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
		}
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Wait waits for the process to end on its own (a one-shot engine command)
// and kills it when ctx ends first. It returns nil only for a clean exit.
func (w *Worker) Wait(ctx context.Context) error {
	select {
	case <-w.done:
	case <-ctx.Done():
		w.Kill()
		<-w.done
		return fmt.Errorf("%w: %v", ErrFailed, ctx.Err())
	}
	if w.waitErr != nil {
		return ErrFailed
	}
	return nil
}

func (w *Worker) keepLast(line []byte) {
	w.lastMu.Lock()
	w.last = bytes.TrimRight(append(w.last[:0], line...), "\r\n")
	w.lastMu.Unlock()
}

// ExitCode is the process's exit code once it has exited: 0, the engine's own code, or -1 when it
// was killed or did not exit normally (and before it exited).
func (w *Worker) ExitCode() int {
	select {
	case <-w.done:
	default:
		return -1
	}
	if w.waitErr == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(w.waitErr, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// LastLine is the last line the process wrote to stdout, read to the end of its output (nil when
// ctx ends first or it wrote none). For one-shot runs; a Call reads its answers itself.
func (w *Worker) LastLine(ctx context.Context) []byte {
	select {
	case <-w.readDone:
	case <-ctx.Done():
		return nil
	}
	w.lastMu.Lock()
	defer w.lastMu.Unlock()
	return append([]byte(nil), w.last...)
}

// Pid is the process id, for resource accounting.
func (w *Worker) Pid() int { return w.cmd.Process.Pid }

// Done is closed when the process has exited.
func (w *Worker) Done() <-chan struct{} { return w.done }

// Stderr is the last few KiB the engine wrote to its error output.
func (w *Worker) Stderr() string { return w.stderr.String() }

// Kill stops the process now.
func (w *Worker) Kill() {
	w.killMu.Do(func() {
		if w.cmd.Process != nil {
			killWorkerProcess(w.cmd)
		}
	})
}

// Close asks the engine to finish by closing its input, and kills it when it
// does not within a moment.
func (w *Worker) Close() error {
	_ = w.stdin.Close()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		w.Kill()
		<-w.done
	}
	return nil
}

// tail keeps the last max bytes written.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
