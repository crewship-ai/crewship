//go:build !windows

package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// #1043: assertMemoryFile Lstat-rejects a pre-existing final-component
// symlink, but the subsequent os.ReadFile on memory.read / memory.write-append
// followed symlinks and had no O_NOFOLLOW / regular-file guard — the TOCTOU
// window between the Lstat check and the read let an agent swap a regular
// AGENT.md for a symlink to a mounted secret. The fix routes both reads
// through readRegularNoFollow (O_NOFOLLOW | O_NONBLOCK + IsRegular check), the
// same primitive the indexer already uses.
//
// A FIFO is the deterministic proxy for the class assertMemoryFile lets
// through: it is NOT a symlink (so the Lstat containment check passes), but it
// is NOT a regular file either. With the old os.ReadFile, open(2) on a FIFO
// with no writer BLOCKS forever (a soft DoS). readRegularNoFollow opens
// non-blocking and rejects the non-regular file fast. The test asserts the
// dispatch returns (IsError) within a deadline — old code times out, fixed
// code returns immediately.

func dispatchWithin(t *testing.T, d *Dispatcher, call ToolCall, within time.Duration) ToolResult {
	t.Helper()
	type outcome struct {
		res ToolResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := d.Dispatch(context.Background(), call)
		ch <- outcome{res, err}
	}()
	select {
	case o := <-ch:
		if o.err != nil {
			t.Fatalf("dispatch: %v", o.err)
		}
		return o.res
	case <-time.After(within):
		t.Fatalf("dispatch blocked >%s — read path followed a non-regular file (missing O_NONBLOCK no-follow guard)", within)
		return ToolResult{}
	}
}

func TestDispatch_Read_RejectsNonRegularFile_NoFollow(t *testing.T) {
	actx := testAgentCtx(t)
	d := NewDispatcher(actx)

	// Plant AGENT.md as a FIFO. Not a symlink → assertMemoryFile's Lstat
	// containment check passes; the read must still refuse it.
	fifo := filepath.Join(actx.AgentMemoryDir, "AGENT.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	res := dispatchWithin(t, d, ToolCall{
		Name: "memory.read",
		Args: json.RawMessage(`{"tier":"AGENT"}`),
	}, 3*time.Second)

	if !res.IsError {
		t.Fatalf("read of a FIFO must yield IsError=true; got content %q", res.Content)
	}
}

func TestDispatch_WriteAppend_RejectsNonRegularFile_NoFollow(t *testing.T) {
	actx := testAgentCtx(t)
	d := NewDispatcher(actx)

	fifo := filepath.Join(actx.AgentMemoryDir, "AGENT.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	// append mode reads the current on-disk body before appending — that read
	// must also refuse the FIFO rather than block on it.
	res := dispatchWithin(t, d, ToolCall{
		Name: "memory.write",
		Args: json.RawMessage(`{"tier":"AGENT","mode":"append","content":"x"}`),
	}, 3*time.Second)

	if !res.IsError {
		t.Fatalf("append over a FIFO must yield IsError=true; got content %q", res.Content)
	}
	// The FIFO must be untouched (no write-through).
	fi, err := os.Lstat(fifo)
	if err != nil {
		t.Fatalf("lstat fifo: %v", err)
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("append replaced the FIFO (mode now %v) — should have refused", fi.Mode())
	}
}
