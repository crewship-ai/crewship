package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestContainerGone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"random", errors.New("connection reset"), false},
		{"no_such_container", errors.New(`Error response from daemon: No such container: d89840badfe6`), true},
		{"is_not_running", errors.New(`Container d89840 is not running`), true},
		{"wrapped_no_such", fmt.Errorf("container stats: %w", errors.New("No such container: abc")), true},
		{"removal_of_container", errors.New(`removal of container d89840badfe6 is already in progress`), true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := containerGone(c.err); got != c.want {
				t.Errorf("containerGone(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// containerGoneMock returns a sentinel "No such container" error on
// ContainerStats so we can drive StatsCollector.poll into the
// auto-unregister path without touching real Docker.
type containerGoneMock struct {
	mockContainer
	calls atomic.Int32
}

func (m *containerGoneMock) ContainerStats(_ context.Context, id string) (*provider.ContainerMetrics, error) {
	m.calls.Add(1)
	return nil, fmt.Errorf("container stats: No such container: %s", id)
}

// TestStatsCollector_UnregistersVanishedContainer pins issue #534. Once
// Docker reports a tracked container is gone, the stats poller must
// drop it from the tracked set rather than spinning on the dead ID
// every interval (the prior behavior produced 15-sec-cadence
// "No such container" debug spam forever).
func TestStatsCollector_UnregistersVanishedContainer(t *testing.T) {
	t.Parallel()
	mc := &containerGoneMock{}
	sc := NewStatsCollector(mc, nil, newSilentLogger(), 50*time.Millisecond)

	sc.Register("vanished-id", "crew-1", "ws-1")
	if got := len(sc.Tracked()); got != 1 {
		t.Fatalf("Tracked() before poll = %d, want 1", got)
	}

	sc.poll(context.Background())
	// poll dispatches to a goroutine and waits on the semaphore, but
	// the per-container goroutine itself doesn't synchronize with us
	// after Unregister. Wait briefly for the goroutine to drain rather
	// than racing.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sc.Tracked()) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := len(sc.Tracked()); got != 0 {
		t.Fatalf("Tracked() after vanished-container poll = %d, want 0 (stats should auto-unregister)", got)
	}
	if mc.calls.Load() == 0 {
		t.Error("ContainerStats was never called; test setup is wrong")
	}
}
