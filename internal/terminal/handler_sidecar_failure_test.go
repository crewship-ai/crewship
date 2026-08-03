package terminal

// The one place a sidecar failure must NOT close the door.
//
// Every other crew-start path treats "the crew's declared database did not come
// up" as fatal, because the work it was about to do assumed the database. The
// terminal is the surface an operator opens to find out WHY it did not come up —
// refusing to open the shell over the very fault the operator is investigating
// leaves them with a message and no way in. The runtime container is up by then;
// only the sidecar half failed.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// brokenSidecarContainer starts the runtime fine and fails every sidecar.
type brokenSidecarContainer struct {
	*imgContainer
	mu    sync.Mutex
	asked int
}

func (c *brokenSidecarContainer) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	c.mu.Lock()
	c.asked++
	c.mu.Unlock()
	return nil, errors.New("port already allocated")
}
func (c *brokenSidecarContainer) StopCrewServices(context.Context, string) error   { return nil }
func (c *brokenSidecarContainer) RemoveCrewServices(context.Context, string) error { return nil }

var _ provider.SidecarProvider = (*brokenSidecarContainer)(nil)
var _ provider.ContainerProvider = (*brokenSidecarContainer)(nil)

func (c *brokenSidecarContainer) sidecarAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.asked
}

func TestTerminalOpensEvenWhenTheCrewsSidecarsFail(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	mustExec(t, db, `UPDATE crews SET services_json = ? WHERE id = 'c1'`,
		`[{"name":"redis","image":"redis:7-alpine"}]`)

	ctr := &brokenSidecarContainer{imgContainer: &imgContainer{}}
	h := New(ctr, v, db, silentLogger(), nil)

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// Read every frame the handler sends until it closes the session, so the
	// assertions below see the whole exchange rather than a race with it.
	var messages []string
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		messages = append(messages, string(data))
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("terminal session did not finish")
	}

	if ctr.sidecarAttempts() == 0 {
		t.Fatal("the terminal must still try to start the crew's declared sidecars")
	}
	joined := strings.Join(messages, "\n")
	if strings.Contains(joined, "failed to start container") {
		t.Errorf("the shell was refused over a sidecar failure — the terminal is where an "+
			"operator diagnoses that failure, and the runtime container was already up.\nframes:\n%s",
			joined)
	}
	// It got past the container start: this provider has no ExecInteractive, so
	// the session ends on the capability check that comes after it.
	if !strings.Contains(joined, "terminal not supported by container provider") {
		t.Errorf("expected the session to reach the interactive-exec capability check, got:\n%s", joined)
	}
}
