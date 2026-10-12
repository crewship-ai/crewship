package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestRuntimeUseIdleStopRequiresConfirmedState(t *testing.T) {
	for _, status := range []string{"running", "", "dead", "removing", "exited", "created", "paused", "restarting"} {
		t.Run(status, func(t *testing.T) {
			var stops atomic.Int32
			p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/json"):
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id": "runtime", "State": map[string]any{"Running": true, "Status": status},
						"Config": map[string]any{"Labels": map[string]string{
							resourcelifecycle.InstanceLabel: "installation", crewCrewIDLabel: "crew", crewKindLabel: crewRuntimeKind,
						}},
					})
				case strings.HasSuffix(r.URL.Path, "/stop"):
					stops.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			t.Cleanup(cleanup)
			p.cfg.InstanceID = "installation"
			p.SetRuntimeIdleVerifier(func(context.Context, string, string) error { return nil })
			stopped, err := p.StopUnusedCrewRuntime(t.Context(), "crew", "runtime")
			if status == "running" {
				if !stopped || err != nil || stops.Load() != 1 {
					t.Fatalf("confirmed idle stop: stopped=%v err=%v stops=%d", stopped, err, stops.Load())
				}
			} else if stopped || !errors.Is(err, provider.ErrRuntimeImageUpdatePending) || stops.Load() != 0 {
				t.Fatalf("unconfirmed state stopped: stopped=%v err=%v stops=%d", stopped, err, stops.Load())
			}
		})
	}
}

func TestRuntimeUseIdleStopCannotBypassReservation(t *testing.T) {
	p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("reserved runtime contacted for idle stop: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	t.Cleanup(cleanup)
	release, err := p.runtimeUseGate("crew").Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stopped, err := p.StopUnusedCrewRuntime(t.Context(), "crew", "runtime")
	if stopped || err != nil {
		t.Fatalf("active reservation ignored: stopped=%v err=%v", stopped, err)
	}
}

func TestRuntimeUseReconcileNeverForceRemoves(t *testing.T) {
	for _, status := range []string{"running", "restarting", "", "exited", "created"} {
		t.Run(status, func(t *testing.T) {
			var removes atomic.Int32
			p, cleanup := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/json"):
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "runtime", "State": map[string]any{"Status": status, "Running": status == "running"}})
				case r.Method == http.MethodDelete:
					if force := r.URL.Query().Get("force"); force == "true" || force == "1" {
						t.Error("forced removal bypassed admission")
					}
					removes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			t.Cleanup(cleanup)
			ctx := context.WithValue(t.Context(), managedRuntimeUseKey{}, true)
			err := p.removeForReconcile(ctx, "runtime", "crew")
			if status == "exited" || status == "created" {
				if err != nil || removes.Load() != 1 {
					t.Fatalf("inactive reconciliation failed: %v removes=%d", err, removes.Load())
				}
			} else if !errors.Is(err, provider.ErrRuntimeImageUpdatePending) || removes.Load() != 0 {
				t.Fatalf("unconfirmed runtime removed: %v removes=%d", err, removes.Load())
			}
		})
	}
}
