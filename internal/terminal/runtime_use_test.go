package terminal

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type reservedTerminalContainer struct {
	*covInteractive
	holds                        *atomic.Int32
	acquired, retained, released atomic.Int32
}

func (c *reservedTerminalContainer) AcquireCrewRuntimeUse(_ context.Context, cfg provider.CrewConfig) (*provider.RuntimeUse, error) {
	c.acquired.Add(1)
	if c.holds.Load() != 0 {
		return nil, errors.New("terminal hold vetoes its own activation")
	}
	c.covContainer.mu.Lock()
	c.states = []string{"running"}
	c.covContainer.mu.Unlock()
	return provider.NewRuntimeUse(cfg.ID, "new-runtime", func() { c.released.Add(1) }), nil
}
func (c *reservedTerminalContainer) RetainCrewRuntimeUse(ctx context.Context, crewID, containerID string) (*provider.RuntimeUse, error) {
	c.retained.Add(1)
	if !provider.RuntimeUseNoWait(ctx) {
		return nil, errors.New("terminal can wait behind work it needs to inspect")
	}
	return provider.NewRuntimeUse(crewID, containerID, func() { c.released.Add(1) }), nil
}
func TestTerminalRuntimeReservationPreservesCurrentWork(t *testing.T) {
	for _, status := range []string{"running", "stopped"} {
		t.Run(status, func(t *testing.T) {
			v := newTestValidator(t)
			db := seedTerminalDB(t)
			serverSide, testSide := net.Pipe()
			defer testSide.Close()
			var holds atomic.Int32
			c := &reservedTerminalContainer{covInteractive: &covInteractive{covContainer: &covContainer{states: []string{status}}, conn: serverSide}, holds: &holds}
			h := New(c, v, db, silentLogger(), nil)
			h.SetContainerHolder(func(string) func() { holds.Add(1); return func() { holds.Add(-1) } })
			conn, done := dialTerminalDone(t, h)
			defer conn.Close()
			authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})
			go func() {
				testSide.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_, _ = testSide.Write([]byte("ready"))
			}()
			if got := string(readBinaryFrame(t, conn)); got != "ready" {
				t.Fatalf("PTY output=%q", got)
			}
			if status == "running" && (c.acquired.Load() != 0 || c.retained.Load() != 1) {
				t.Fatal("terminal tried to activate the pending image instead of retaining current work")
			}
			if status == "stopped" && (c.acquired.Load() != 1 || c.retained.Load() != 0) {
				t.Fatal("stopped runtime was not acquired")
			}
			if holds.Load() != 1 || c.released.Load() != 0 {
				t.Fatal("terminal lifetime is not reserved")
			}
			conn.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal failed to close")
			}
			if holds.Load() != 0 || c.released.Load() != 1 {
				t.Fatal("closed terminal leaked reservation")
			}
		})
	}
}
