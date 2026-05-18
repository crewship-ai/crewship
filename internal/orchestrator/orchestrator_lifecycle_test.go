package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// orchestrator_lifecycle.go — GetOrCreateContainer, Start, checkTTLs.
//
// Existing tests cover StopAccepting and most of RecoverFromCrash;
// this fills the container-resolve, ticker-loop, and TTL-sweep gaps.
// ---------------------------------------------------------------------------

// lifecycleFakeContainer is a tiny ContainerProvider that records the
// last EnsureCrewRuntime and StopCrewRuntime calls so tests can pin
// the dispatched args.
type lifecycleFakeContainer struct {
	mu sync.Mutex

	ensureCalls       int
	ensureCfg         provider.CrewConfig
	ensureReturnID    string
	ensureReturnErr   error

	stopCalls       int
	stopContainerID string
	stopReturnErr   error
}

func (f *lifecycleFakeContainer) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	f.ensureCfg = cfg
	return f.ensureReturnID, f.ensureReturnErr
}
func (f *lifecycleFakeContainer) StopCrewRuntime(_ context.Context, containerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	f.stopContainerID = containerID
	return f.stopReturnErr
}
func (f *lifecycleFakeContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (f *lifecycleFakeContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return nil, nil
}
func (f *lifecycleFakeContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (f *lifecycleFakeContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (f *lifecycleFakeContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (f *lifecycleFakeContainer) CrewContainerName(slug string) string { return "crewship-team-" + slug }
func (f *lifecycleFakeContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

var _ provider.ContainerProvider = (*lifecycleFakeContainer)(nil)

func quietLifecycleLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// ---- GetOrCreateContainer ----

func TestGetOrCreateContainer_NilProvider_Errors(t *testing.T) {
	o := New(nil, nil, quietLifecycleLogger())
	_, err := o.GetOrCreateContainer(context.Background(), "alpha", "crew-a", "ws-1")
	if err == nil {
		t.Fatal("expected error when container provider is nil")
	}
	if !strings.Contains(err.Error(), "container provider not configured") {
		t.Errorf("err = %v, want \"container provider not configured\"", err)
	}
}

func TestGetOrCreateContainer_ProviderError_WrappedWithCrewWorkspaceContext(t *testing.T) {
	// Source wraps the underlying error with crew + workspace ids so an
	// operator triaging a "ensure crew runtime" failure can find both
	// without grepping logs.
	fake := &lifecycleFakeContainer{ensureReturnErr: errors.New("docker unreachable")}
	o := New(fake, nil, quietLifecycleLogger())
	_, err := o.GetOrCreateContainer(context.Background(), "alpha", "crew-a", "ws-1")
	if err == nil {
		t.Fatal("expected error from provider")
	}
	if !strings.Contains(err.Error(), "crew-a") || !strings.Contains(err.Error(), "ws-1") {
		t.Errorf("err = %v, want both crew + workspace ids", err)
	}
}

func TestGetOrCreateContainer_HappyPath_RegistersStatsWhenWired(t *testing.T) {
	fake := &lifecycleFakeContainer{ensureReturnID: "container-abc"}
	o := New(fake, nil, quietLifecycleLogger())
	var statsCalled int
	var gotContainerID, gotCrewID, gotWorkspaceID string
	o.SetStatsRegisterCallback(func(containerID, crewID, workspaceID string) {
		statsCalled++
		gotContainerID = containerID
		gotCrewID = crewID
		gotWorkspaceID = workspaceID
	})

	id, err := o.GetOrCreateContainer(context.Background(), "alpha", "crew-a", "ws-1")
	if err != nil {
		t.Fatalf("GetOrCreateContainer: %v", err)
	}
	if id != "container-abc" {
		t.Errorf("id = %q, want container-abc", id)
	}
	if fake.ensureCfg.ID != "crew-a" || fake.ensureCfg.Slug != "alpha" {
		t.Errorf("EnsureCrewRuntime cfg = %+v, want {ID:crew-a, Slug:alpha}", fake.ensureCfg)
	}
	if statsCalled != 1 {
		t.Errorf("stats callback called %d times, want 1", statsCalled)
	}
	if gotContainerID != "container-abc" || gotCrewID != "crew-a" || gotWorkspaceID != "ws-1" {
		t.Errorf("stats callback args = (%q, %q, %q), want (container-abc, crew-a, ws-1)",
			gotContainerID, gotCrewID, gotWorkspaceID)
	}
}

func TestGetOrCreateContainer_EmptyWorkspaceID_SkipsStatsRegistration(t *testing.T) {
	// Source guard: stats register only fires when reg != nil AND
	// workspaceID != "". Pin the empty-workspace skip so mission-style
	// runs that don't yet know their workspace can still resolve a
	// container without polluting stats with an empty-ws subscription.
	fake := &lifecycleFakeContainer{ensureReturnID: "ct-2"}
	o := New(fake, nil, quietLifecycleLogger())
	var statsCalls int
	o.SetStatsRegisterCallback(func(_, _, _ string) { statsCalls++ })

	if _, err := o.GetOrCreateContainer(context.Background(), "alpha", "crew-a", ""); err != nil {
		t.Fatalf("GetOrCreateContainer: %v", err)
	}
	if statsCalls != 0 {
		t.Errorf("stats called %d times with empty workspaceID; want 0", statsCalls)
	}
}

func TestGetOrCreateContainer_NoStatsCallback_DoesNotPanic(t *testing.T) {
	// When SetStatsRegisterCallback was never called, statsRegister
	// stays nil. GetOrCreateContainer must still succeed without
	// dereferencing nil.
	fake := &lifecycleFakeContainer{ensureReturnID: "ct-3"}
	o := New(fake, nil, quietLifecycleLogger())
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil stats callback panicked: %v", r)
		}
	}()
	id, err := o.GetOrCreateContainer(context.Background(), "alpha", "crew-a", "ws-1")
	if err != nil {
		t.Fatalf("GetOrCreateContainer: %v", err)
	}
	if id != "ct-3" {
		t.Errorf("id = %q, want ct-3", id)
	}
}

// ---- Start ----

func TestStart_ReturnsContextErr_OnCancel(t *testing.T) {
	// Start's ticker fires every 5 minutes; the only test surface is
	// the ctx.Done() branch. Pre-cancel the ctx and verify Start
	// returns ctx.Err promptly.
	o := New(nil, nil, quietLifecycleLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- o.Start(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of cancelled ctx")
	}
}

// ---- checkTTLs ----

func TestCheckTTLs_NoCrews_NoOp(t *testing.T) {
	fake := &lifecycleFakeContainer{}
	o := New(fake, nil, quietLifecycleLogger())
	o.checkTTLs(context.Background())
	if fake.stopCalls != 0 {
		t.Errorf("StopCrewRuntime called %d times on empty crew map; want 0", fake.stopCalls)
	}
}

func TestCheckTTLs_CrewWithZeroTTL_Skipped(t *testing.T) {
	// ttl=0 means "no expiry"; checkTTLs must skip it entirely. Pin
	// that the crew stays in the map AND no Stop call fires.
	fake := &lifecycleFakeContainer{}
	o := New(fake, nil, quietLifecycleLogger())
	o.crews["crew-immortal"] = &crewState{
		lastActivity: time.Now().Add(-100 * time.Hour),
		ttl:          0,
		containerID:  "ct-immortal",
	}
	o.checkTTLs(context.Background())
	if fake.stopCalls != 0 {
		t.Errorf("zero-TTL crew was stopped; want skip")
	}
	if _, kept := o.crews["crew-immortal"]; !kept {
		t.Error("zero-TTL crew evicted from map; want preserved")
	}
}

func TestCheckTTLs_ExpiredCrew_StoppedAndEvicted(t *testing.T) {
	fake := &lifecycleFakeContainer{}
	o := New(fake, nil, quietLifecycleLogger())
	o.crews["crew-stale"] = &crewState{
		lastActivity: time.Now().Add(-2 * time.Hour),
		ttl:          1 * time.Hour,
		containerID:  "ct-stale",
	}

	o.checkTTLs(context.Background())

	if fake.stopCalls != 1 {
		t.Errorf("StopCrewRuntime called %d times, want 1", fake.stopCalls)
	}
	if fake.stopContainerID != "ct-stale" {
		t.Errorf("stopped container = %q, want ct-stale", fake.stopContainerID)
	}
	if _, present := o.crews["crew-stale"]; present {
		t.Error("expired crew still in map; want evicted")
	}
}

func TestCheckTTLs_FreshCrew_PreservedNoStop(t *testing.T) {
	fake := &lifecycleFakeContainer{}
	o := New(fake, nil, quietLifecycleLogger())
	o.crews["crew-fresh"] = &crewState{
		lastActivity: time.Now().Add(-1 * time.Minute),
		ttl:          1 * time.Hour,
		containerID:  "ct-fresh",
	}
	o.checkTTLs(context.Background())
	if fake.stopCalls != 0 {
		t.Errorf("fresh crew was stopped; want skip")
	}
	if _, kept := o.crews["crew-fresh"]; !kept {
		t.Error("fresh crew evicted; want preserved")
	}
}

func TestCheckTTLs_ExpiredCrewEmptyContainerID_EvictedButNoStop(t *testing.T) {
	// Source guard inside the stop loop: "if stop.containerID == ""
	// continue". An expired crew whose container was never recorded
	// must still be evicted from the map (the prior loop deletes it)
	// but Stop must NOT be called with an empty id.
	fake := &lifecycleFakeContainer{}
	o := New(fake, nil, quietLifecycleLogger())
	o.crews["crew-empty"] = &crewState{
		lastActivity: time.Now().Add(-2 * time.Hour),
		ttl:          1 * time.Hour,
		containerID:  "", // never recorded
	}
	o.checkTTLs(context.Background())
	if fake.stopCalls != 0 {
		t.Errorf("StopCrewRuntime called with empty containerID; want skipped")
	}
	if _, present := o.crews["crew-empty"]; present {
		t.Error("expired-no-container crew still in map; want evicted")
	}
}

func TestCheckTTLs_StopError_LoggedButContinues(t *testing.T) {
	// StopCrewRuntime returning an error must not abort the sweep —
	// the crew is already evicted from the map, and other expired
	// crews in the same tick should still get their stop call.
	fake := &lifecycleFakeContainer{stopReturnErr: errors.New("daemon refused")}
	o := New(fake, nil, quietLifecycleLogger())
	o.crews["a"] = &crewState{
		lastActivity: time.Now().Add(-2 * time.Hour),
		ttl:          1 * time.Hour,
		containerID:  "ct-a",
	}
	o.crews["b"] = &crewState{
		lastActivity: time.Now().Add(-2 * time.Hour),
		ttl:          1 * time.Hour,
		containerID:  "ct-b",
	}
	o.checkTTLs(context.Background())
	if fake.stopCalls != 2 {
		t.Errorf("StopCrewRuntime called %d times, want 2 (both expired crews stopped even with errors)", fake.stopCalls)
	}
	if _, ok := o.crews["a"]; ok {
		t.Error("crew a still in map after expiry")
	}
	if _, ok := o.crews["b"]; ok {
		t.Error("crew b still in map after expiry")
	}
}
