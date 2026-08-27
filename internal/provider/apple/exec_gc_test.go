package apple

// Tests for exec-entry garbage collection.
//
// The sweeper deleted an entry the moment its process finished, and the whole
// point of an entry is that ExecInspect can be asked about it AFTERWARDS —
// every caller runs a command and then inspects it. A sweep landing in that
// window turned a completed exec into "exec ... not found" with exit code -1,
// i.e. a spurious failure for a command that succeeded. Finished entries now
// linger for a grace period first.

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// finishedExec runs a trivial exec to completion and returns its ID.
func finishedExec(t *testing.T, p *Provider) string {
	t.Helper()
	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"true"},
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()
	waitExecDone(t, p, res.ExecID)
	return res.ExecID
}

func TestCollectFinishedExecs_KeepsEntryDuringGracePeriod(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	execID := finishedExec(t, p)

	// A sweep that fires right after the process exits — the exact race the
	// caller loses — must not take the entry away.
	p.collectFinishedExecs(time.Now())

	running, code, err := p.ExecInspect(context.Background(), execID)
	if err != nil {
		t.Fatalf("ExecInspect right after a sweep: %v (the entry was collected before its caller could read it)", err)
	}
	if running || code != 0 {
		t.Errorf("inspect = (running=%v, code=%d), want finished with code 0", running, code)
	}
}

func TestCollectFinishedExecs_CollectsAfterGracePeriod(t *testing.T) {
	installFakeContainer(t, `exit 0`)
	p := newTestProvider(Config{})

	execID := finishedExec(t, p)

	p.collectFinishedExecs(time.Now().Add(execRetention + time.Second))

	if _, _, err := p.ExecInspect(context.Background(), execID); err == nil {
		t.Fatal("entry survived past its retention window; finished execs must not accumulate")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want a not-found error", err)
	}
}

// A `container` invocation can leave a descendant behind, and that descendant
// inherits the exec's stdout and stderr. Both are an execSpool here — an
// io.Writer, not an *os.File — so os/exec gives the child a real pipe and
// copies out of it in a goroutine, and cmd.Wait blocks on that goroutine until
// EVERY write end of the pipe is closed. The orphan holds one.
//
// So Wait blocked for as long as the orphan lived, and nothing downstream
// recovered: registerExec closes entry.done only after Wait returns, so
// ExecInspect answered "running" forever and collectFinishedExecs never
// reclaimed the entry (#2037).
//
// The fake CLI backgrounds a sleep far longer than this test is willing to
// wait, so an exec that finishes here can only have finished because Wait is
// bounded.
func TestExec_OrphanHoldingOutputPipesDoesNotPinTheEntryForever(t *testing.T) {
	installFakeContainer(t, `
sleep 60 &
echo hi
exit 0`)
	p := newTestProvider(Config{})

	res, err := p.Exec(context.Background(), provider.ExecConfig{
		ContainerID: "cid-1",
		Cmd:         []string{"true"},
		User:        "1001:1001",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// Deliberately not draining the reader first: the spool exists so the
	// process finishes whether or not anyone reads, and draining here would
	// hide a wedged Wait behind a blocked Read.
	started := time.Now()
	deadline := started.Add(execWaitDelay + 5*time.Second)
	var code int
	for {
		running, c, err := p.ExecInspect(context.Background(), res.ExecID)
		if err != nil {
			t.Fatalf("ExecInspect: %v", err)
		}
		if !running {
			code = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec still reported as running %s after the CLI exited: a descendant holding the output pipes is pinning cmd.Wait, so the entry never finishes and never becomes collectable", time.Since(started).Round(time.Millisecond))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Unblocking Wait must not cost us the status the kernel already gave us:
	// the CLI exited 0 and that is what the caller has to see, not the -1 of
	// "no usable status".
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read exec output: %v", err)
	}
	if !strings.Contains(string(out), "hi") {
		t.Errorf("output = %q, want the CLI's output to survive", out)
	}

	// The entry is a real collection candidate now, not one the sweeper has to
	// skip forever.
	p.collectFinishedExecs(time.Now().Add(execRetention + time.Second))
	if _, _, err := p.ExecInspect(context.Background(), res.ExecID); err == nil {
		t.Error("entry survived past its retention window; a wedged exec is never reclaimed")
	}
}

// An exec that is still running is never a collection candidate, whatever the
// clock says.
func TestCollectFinishedExecs_KeepsRunningEntry(t *testing.T) {
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

	p.collectFinishedExecs(time.Now().Add(24 * time.Hour))

	if _, _, err := p.ExecInspect(context.Background(), res.ExecID); err != nil {
		t.Fatalf("running exec was collected: %v", err)
	}

	stdinW.Close()
	waitExecDone(t, p, res.ExecID)
}
