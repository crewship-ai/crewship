package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// daemonMode is what the fake daemon answers to the sentinel create.
type daemonMode int32

const (
	daemonRejects daemonMode = iota
	daemonAccepts            // 404 No such image: the mounts validated
	daemonFails              // 500: no opinion
	daemonCreates            // 201: a container exists and is removed again
)

type readyDaemon struct {
	mode    atomic.Int32
	creates atomic.Int32
	removes atomic.Int32
}

func (d *readyDaemon) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/containers/create"):
		d.creates.Add(1)
		switch daemonMode(d.mode.Load()) {
		case daemonRejects:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"invalid mount config for type \"bind\": bind source path does not exist: /fake/sidecar"}`))
		case daemonAccepts:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such image: ` + preflightSentinelImage + `"}`))
		case daemonCreates:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"probe-container-id"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"daemon busy"}`))
		}
	case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
		d.removes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"unexpected"}`))
	}
}

func readyProvider(t *testing.T, inContainer, explicit bool) (*Provider, *readyDaemon) {
	t.Helper()
	d := &readyDaemon{}
	p, cleanup := newFakeDockerProvider(t, d.handle)
	t.Cleanup(cleanup)
	p.cfg = Config{ReportRuntimeReadiness: explicit, SidecarBinaryPath: "/fake/sidecar", EntrypointPath: "/fake/entrypoint.sh"}
	p.inContainerFn = func() bool { return inContainer }
	return p, d
}

func age(p *Provider, rejected bool, by time.Duration) {
	p.bindVerdict.Store(&bindVerdict{rejected: rejected, at: time.Now().Add(-by)})
}

func TestRuntimeReadyOnlyForExplicitDockerInsideAContainer(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		inContainer, explicit bool
	}{
		{"native install, explicit docker", false, true},
		{"auto-selected provider inside a container", true, false},
		{"native install, auto", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d := readyProvider(t, tc.inContainer, tc.explicit)
			d.mode.Store(int32(daemonRejects))
			if err := p.RuntimeReady(context.Background()); err != nil {
				t.Fatalf("a setup outside the containerized-server topology became unready: %v", err)
			}
			if d.creates.Load() != 0 {
				t.Fatalf("the daemon was probed %d times for a setup that does not report", d.creates.Load())
			}
		})
	}
}

func TestRuntimeReadyReportsConfirmedRejectionWithoutPaths(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonRejects))
	err := p.RuntimeReady(context.Background())
	if !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("a rejected mandatory bind source reported %v", err)
	}
	if strings.Contains(err.Error(), "/fake/") {
		t.Fatalf("the unauthenticated readiness reason leaks a path: %v", err)
	}
	// Cached: asking again does not hit the daemon.
	if err := p.RuntimeReady(context.Background()); !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("second answer %v", err)
	}
	if d.creates.Load() != 1 {
		t.Fatalf("a fresh verdict was probed again: %d creates", d.creates.Load())
	}
}

func TestRuntimeReadyClearsByItselfAfterTheFix(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonRejects))
	if p.RuntimeReady(context.Background()) == nil {
		t.Fatal("rejection not reported")
	}
	// The operator fixes the mounts; the daemon now accepts them. The stored
	// rejection is only asked again once its short window has passed.
	d.mode.Store(int32(daemonAccepts))
	if p.RuntimeReady(context.Background()) == nil {
		t.Fatal("a rejection was dropped before it was verified again")
	}
	age(p, true, bindRejectedTTL+time.Second)
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("readiness stayed red after the daemon accepted the binds: %v", err)
	}
}

func TestRuntimeReadyInconclusiveAnswerNeverMakesItUnready(t *testing.T) {
	for _, mode := range []daemonMode{daemonFails} {
		p, d := readyProvider(t, true, true)
		d.mode.Store(int32(mode))
		if err := p.RuntimeReady(context.Background()); err != nil {
			t.Fatalf("an unproven answer turned readiness red: %v", err)
		}
	}
	// A daemon that is not reachable at all is not a verdict either.
	p, cleanup := newFakeDockerProvider(t, func(http.ResponseWriter, *http.Request) {})
	cleanup() // closes the server
	p.cfg = Config{ReportRuntimeReadiness: true, SidecarBinaryPath: "/fake/sidecar", EntrypointPath: "/fake/entrypoint.sh"}
	p.inContainerFn = func() bool { return true }
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("an unreachable daemon was reported as a bind rejection: %v", err)
	}
}

func TestRuntimeReadyKeepsConfirmedRejectionThroughAnInconclusiveRetry(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonRejects))
	if p.RuntimeReady(context.Background()) == nil {
		t.Fatal("rejection not reported")
	}
	d.mode.Store(int32(daemonFails))
	age(p, true, bindRejectedTTL+time.Second)
	if err := p.RuntimeReady(context.Background()); !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("an unproven retry erased a confirmed rejection: %v", err)
	}
}

func TestRuntimeReadyRemovesAContainerTheProbeCreated(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonCreates))
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("created probe container reported %v", err)
	}
	if d.removes.Load() != 1 {
		t.Fatalf("probe container removed %d times", d.removes.Load())
	}
}

func TestRuntimeReadyAcceptedMountsAreVerifiedAgainAfterTheirWindow(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonAccepts))
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.mode.Store(int32(daemonRejects))
	if err := p.RuntimeReady(context.Background()); err != nil {
		t.Fatalf("a fresh ok verdict was re-probed: %v", err)
	}
	age(p, false, bindVerifiedTTL+time.Second)
	if err := p.RuntimeReady(context.Background()); !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("a later rejection was not noticed: %v", err)
	}
}

// The boot preflight and the readiness state read the same probe: a rejection
// found at startup is what readiness first reports, without a second request.
func TestBootPreflightSeedsTheReadinessState(t *testing.T) {
	p, d := readyProvider(t, true, true)
	d.mode.Store(int32(daemonRejects))
	p.preflightMandatoryBinds(context.Background())
	if err := p.RuntimeReady(context.Background()); !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("boot rejection not visible to readiness: %v", err)
	}
	if d.creates.Load() != 1 {
		t.Fatalf("%d probes for one verdict", d.creates.Load())
	}
}
