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
