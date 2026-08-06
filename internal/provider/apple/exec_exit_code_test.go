package apple

// Tests for ExecInspect's exit-code truthfulness.
//
// Exec used to hand the caller the read half of a synchronous io.Pipe that the
// process itself wrote into, so cmd.Wait() could not return until the caller
// drained the stream to EOF. Two very ordinary caller shapes therefore got a
// fabricated answer out of ExecInspect:
//
//   - a caller that stops short (routes_files.go copies at most 64 KiB into
//     io.Discard, keeper_execute.go reads a limited prefix and inspects) saw
//     running=true / exitCode=0 — indistinguishable from success;
//   - a caller that closes the reader early made cmd.Wait() return
//     io.ErrClosedPipe, which is not an *exec.ExitError, so the entry was
//     stamped -1 and the process's REAL exit code was lost.
//
// The exit code must be a fact about the process, not about the caller's
// reading habits.

import (
	"context"
	"io"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// chattyExecScript writes far more than a partial reader will take, then exits
// with a distinctive non-zero code.
const chattyExecScript = `
case "$1" in
  exec)
    i=0
    while [ $i -lt 400 ]; do
      echo "line-$i-0123456789012345678901234567890123456789012345678901234567890123456789"
      i=$((i+1))
    done
    exit 7;;
esac
exit 0`

// A caller that reads a prefix and then simply stops must still learn the real
// exit code — this is routes_files.go's io.LimitReader shape.
func TestExecInspect_ReportsRealExitCodeWhenReaderStopsShort(t *testing.T) {
	installFakeContainer(t, chattyExecScript)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"sh"},
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(res.Reader, 64)); err != nil {
		t.Fatalf("partial read: %v", err)
	}

	if code := waitExecDone(t, p, res.ExecID); code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

// A caller that closes the reader early must not turn the process's exit code
// into -1: closing a pipe is the caller's business, the exit status is the
// process's.
func TestExecInspect_ReportsRealExitCodeAfterEarlyReaderClose(t *testing.T) {
	installFakeContainer(t, chattyExecScript)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"sh"},
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	buf := make([]byte, 16)
	if _, err := io.ReadFull(res.Reader, buf); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	if err := res.Reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}

	if code := waitExecDone(t, p, res.ExecID); code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

// A caller that DOES drain still gets every byte the process wrote: buffering
// output must not cost the streaming contract.
func TestExec_FullDrainStillSeesAllOutput(t *testing.T) {
	installFakeContainer(t, chattyExecScript)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"sh"},
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if n := countLines(string(out)); n != 400 {
		t.Errorf("read %d lines, want 400 (streamed output was truncated)", n)
	}
	if code := waitExecDone(t, p, res.ExecID); code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
}

func countLines(s string) int {
	n := 0
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	return n
}

// While the process is still running there is no exit code yet, and 0 is the
// one value that must not be invented: callers that discard the running flag
// (routes_files.go, keeper_execute.go, exec_sidecar.go) read it as success.
func TestExecInspect_RunningReportsFailClosedSentinel(t *testing.T) {
	installFakeContainer(t, `
case "$1" in
  exec) read line; exit 0;;
esac
exit 0`)
	p := newTestProvider(Config{})

	stdinR, stdinW := io.Pipe()
	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"sh"},
		User:        "1001:1001",
		Stdin:       stdinR,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// The script blocks on `read` until stdin closes, so the exec is
	// unambiguously still running here.
	running, code, err := p.ExecInspect(context.Background(), res.ExecID)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if !running {
		t.Fatal("exec reported finished while blocked on stdin")
	}
	if code == 0 {
		t.Errorf("in-flight exec reported exit code 0, which callers that ignore the running flag read as success")
	}
	if code != execRunningExitCode {
		t.Errorf("in-flight exit code = %d, want the %d sentinel", code, execRunningExitCode)
	}

	stdinW.Close()
	if code := waitExecDone(t, p, res.ExecID); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}
