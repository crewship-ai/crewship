package terminal

// An attached terminal is one of the four things that live inside a crew
// container and that an idle-TTL stop would destroy (#1662). A shell sitting
// at a prompt produces no agent runs, so nothing refreshes the crew's
// activity clock — under a four-hour default a long debugging session would
// have the container pulled out from under it.

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeHTTP_ShellSessionHoldsTheCrewContainer(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), nil)

	var live atomic.Int32
	var crewIDs atomic.Value
	h.SetContainerHolder(func(crewID string) func() {
		crewIDs.Store(crewID)
		live.Add(1)
		return func() { live.Add(-1) }
	})

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// Wait for the exec to be wired — by then the hold must be live.
	deadline := time.Now().Add(3 * time.Second)
	for im.config() == nil {
		if time.Now().After(deadline) {
			t.Fatal("ExecInteractive never called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := live.Load(); got != 1 {
		t.Fatalf("live holds during an attached terminal = %d, want 1", got)
	}
	if got, _ := crewIDs.Load().(string); got != "c1" {
		t.Errorf("hold taken for crew %q, want c1", got)
	}

	conn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after client close")
	}
	if got := live.Load(); got != 0 {
		t.Errorf("live holds after the terminal closed = %d, want 0", got)
	}
}

func TestServeHTTP_NoContainerHolderWired_SessionStillRuns(t *testing.T) {
	// The holder is optional (dev/no-orchestrator paths). A nil hook must not
	// turn a working terminal into a panic.
	v := newTestValidator(t)
	db := seedTerminalDB(t)

	serverSide, _ := net.Pipe()
	im := &covInteractive{covContainer: &covContainer{states: []string{"running"}}, conn: serverSide}
	h := New(im, v, db, silentLogger(), nil)

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	deadline := time.Now().Add(3 * time.Second)
	for im.config() == nil {
		if time.Now().After(deadline) {
			t.Fatal("ExecInteractive never called")
		}
		time.Sleep(5 * time.Millisecond)
	}

	conn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after client close")
	}
}
