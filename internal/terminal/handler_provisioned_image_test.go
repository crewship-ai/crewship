package terminal

// #1717, as reported: "opening the web terminal on a fully provisioned crew
// starts a container from debian:bookworm-slim — no gh, no claude, no node".
//
// The terminal asked the provider for provider.CrewConfig{ID, Slug} and nothing
// else, so the provider fell back to the global default runtime image. This
// asserts the image the provider is actually asked for, which is the thing that
// decides what the operator's shell lands in.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// imgContainer reports the crew as stopped once (so the handler starts it) and
// records every CrewConfig it is asked to start.
type imgContainer struct {
	mu       sync.Mutex
	asked    []provider.CrewConfig
	statuses int
}

func (m *imgContainer) EnsureCrewRuntime(_ context.Context, c provider.CrewConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, c)
	return "container-" + c.Slug, nil
}
func (m *imgContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (m *imgContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (m *imgContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses++
	if m.statuses == 1 {
		return &provider.ContainerStatus{ID: id, State: "stopped"}, nil
	}
	return &provider.ContainerStatus{ID: id, State: "running"}, nil
}
func (m *imgContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *imgContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(nil)}, nil
}
func (m *imgContainer) ExecInspect(context.Context, string) (bool, int, error) { return false, 0, nil }
func (m *imgContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (m *imgContainer) CopyToContainer(context.Context, string, string, io.Reader) error { return nil }

func (m *imgContainer) started() []provider.CrewConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]provider.CrewConfig, len(m.asked))
	copy(out, m.asked)
	return out
}

func TestTerminalStartsTheCrewsProvisionedImage(t *testing.T) {
	v := newTestValidator(t)
	db := seedTerminalDB(t)
	mustExec(t, db, `UPDATE crews SET cached_image = 'crewship-cache:db6c6fcbdb34' WHERE id = 'c1'`)

	ctr := &imgContainer{}
	h := New(ctr, v, db, silentLogger(), nil)

	conn, done := dialTerminalDone(t, h)
	authAndInit(t, conn, v, map[string]any{"crew_id": "c1", "crew_slug": "crew-a"})

	// The session ends on its own (this provider has no ExecInteractive), which
	// is after the container start we are asserting on.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("terminal session did not finish")
	}

	started := ctr.started()
	if len(started) == 0 {
		t.Fatal("the terminal never asked the provider to start the crew")
	}
	if got := started[0].CachedImage; got != "crewship-cache:db6c6fcbdb34" {
		t.Errorf("CachedImage = %q, want the crew's provisioned tag — empty is how the terminal "+
			"ended up in debian:bookworm-slim with none of the crew's tools (#1717)", got)
	}
}
