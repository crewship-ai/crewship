package servicelifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type lifecycleRuntime struct {
	ensure func(context.Context, provider.CrewConfig) (map[string]string, error)
	stop   func(context.Context, string, string, string) error
}

func (r lifecycleRuntime) EnsureCrewServices(ctx context.Context, cfg provider.CrewConfig) (map[string]string, error) {
	return r.ensure(ctx, cfg)
}
func (r lifecycleRuntime) StopCrewService(ctx context.Context, crew, slug, service string) error {
	return r.stop(ctx, crew, slug, service)
}

func TestControllerRuntimeFailuresAndDeletedCrews(t *testing.T) {
	for _, tt := range []struct {
		name, desired, observed, code       string
		deleted, resolveFails, runtimeFails bool
		wantStarts, wantStops, wantResolves int
	}{
		{name: "runtime start fails", desired: "running", observed: "error", code: "runtime_unavailable", runtimeFails: true, wantStarts: 1, wantResolves: 1},
		{name: "runtime stop fails", desired: "stopped", observed: "error", code: "runtime_unavailable", runtimeFails: true, wantStops: 1},
		{name: "revocation stop fails", desired: "running", observed: "error", code: "runtime_unavailable", resolveFails: true, runtimeFails: true, wantStops: 1, wantResolves: 1},
		{name: "deleted crew stops despite running intent", desired: "running", observed: "stopped", deleted: true, wantStops: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := fenceDB(t)
			if _, err := db.Exec(`UPDATE service_runtime_intents SET desired_state=?`, tt.desired); err != nil {
				t.Fatal(err)
			}
			if tt.deleted {
				if _, err := db.Exec(`UPDATE crews SET deleted_at='deleted'`); err != nil {
					t.Fatal(err)
				}
			}
			starts, stops, resolves := 0, 0, 0
			sensitive := errors.New("synthetic private credential in provider error")
			runtime := lifecycleRuntime{
				ensure: func(ctx context.Context, cfg provider.CrewConfig) (map[string]string, error) {
					starts++
					if cfg.ID != "crew" || len(cfg.Services) != 1 || cfg.Services[0].Name != "database" {
						t.Errorf("resolved configuration lost: %+v", cfg)
					}
					if tt.runtimeFails {
						return nil, sensitive
					}
					return nil, nil
				},
				stop: func(ctx context.Context, crew, slug, service string) error {
					stops++
					if crew != "crew" || slug != "crew" || service != "database" {
						t.Errorf("wrong stop target: %q %q %q", crew, slug, service)
					}
					if tt.runtimeFails {
						return sensitive
					}
					return nil
				},
			}
			c := Controller{DB: db, Runtime: runtime, Resolve: func(ctx context.Context, crew, workspace, service string) (provider.CrewConfig, error) {
				resolves++
				if crew != "crew" || workspace != "workspace" || service != "database" {
					t.Errorf("wrong resolve target: %q %q %q", crew, workspace, service)
				}
				if tt.resolveFails {
					return provider.CrewConfig{}, sensitive
				}
				return provider.CrewConfig{ID: crew, Services: []provider.CrewService{{Name: service}}}, nil
			}}
			before := time.Now()
			c.Reconcile(t.Context())
			var observed, code, owner, until, next string
			if err := db.QueryRow(`SELECT observed_state,last_error,lease_owner,lease_until,next_attempt_at FROM service_runtime_intents`).Scan(&observed, &code, &owner, &until, &next); err != nil {
				t.Fatal(err)
			}
			if observed != tt.observed || code != tt.code || owner != "" || until != "" {
				t.Fatalf("state=%q code=%q lease=%q/%q", observed, code, owner, until)
			}
			retry, err := time.Parse(time.RFC3339Nano, next)
			if err != nil {
				t.Fatal(err)
			}
			minimum := 25 * time.Second
			if tt.observed == "error" {
				minimum = 55 * time.Second
			}
			if retry.Before(before.Add(minimum)) || retry.After(time.Now().Add(65*time.Second)) {
				t.Fatalf("retry deadline not bounded: %s", next)
			}
			c.Reconcile(t.Context())
			if starts != tt.wantStarts || stops != tt.wantStops || resolves != tt.wantResolves {
				t.Fatalf("start/stop/resolve=%d/%d/%d; expected %d/%d/%d; retry must not run early", starts, stops, resolves, tt.wantStarts, tt.wantStops, tt.wantResolves)
			}
		})
	}
}

func TestControllerOldCompletionCannotOverwriteStopCrew(t *testing.T) {
	db, other := fenceDB(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runtime := lifecycleRuntime{
		ensure: func(ctx context.Context, _ provider.CrewConfig) (map[string]string, error) {
			close(entered)
			select {
			case <-release:
				return nil, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		stop: func(context.Context, string, string, string) error { return nil },
	}
	c := Controller{DB: db, Runtime: runtime, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, nil
	}}
	go func() { defer close(done); c.Reconcile(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("runtime did not enter", ctx.Err())
	}
	if err := StopCrew(ctx, other, "crew"); err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("controller did not finish", ctx.Err())
	}
	var desired, observed, owner, until, next string
	var version int
	if err := other.QueryRow(`SELECT desired_state,version,observed_state,lease_owner,lease_until,next_attempt_at FROM service_runtime_intents`).Scan(&desired, &version, &observed, &owner, &until, &next); err != nil {
		t.Fatal(err)
	}
	if desired != "stopped" || version != 5 || observed != "pending" || owner != "" || until != "" || next != "" {
		t.Fatalf("old completion overwrote new stop: desired=%q version=%d observed=%q lease=%q/%q retry=%q", desired, version, observed, owner, until, next)
	}
	c.Reconcile(ctx)
	if err := other.QueryRow(`SELECT observed_state FROM service_runtime_intents`).Scan(&observed); err != nil || observed != "stopped" {
		t.Fatalf("new stop was not reconciled: %q %v", observed, err)
	}
}

func TestControllerCancellationStillReleasesLease(t *testing.T) {
	db, _ := fenceDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := Controller{DB: db, Runtime: lifecycleRuntime{
		ensure: func(opCtx context.Context, _ provider.CrewConfig) (map[string]string, error) {
			cancel()
			<-opCtx.Done()
			return nil, opCtx.Err()
		},
		stop: func(context.Context, string, string, string) error { t.Error("unexpected stop"); return nil },
	}, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, nil
	}}
	c.Reconcile(ctx)
	var observed, code, owner, until string
	if err := db.QueryRow(`SELECT observed_state,last_error,lease_owner,lease_until FROM service_runtime_intents`).Scan(&observed, &code, &owner, &until); err != nil {
		t.Fatal(err)
	}
	if observed != "error" || code != "runtime_unavailable" || owner != "" || until != "" {
		t.Fatalf("cancel stranded a lease: %q %q %q %q", observed, code, owner, until)
	}
}

func TestFilterServicesKeepsOtherCrewsAndCallerConfiguration(t *testing.T) {
	db, _ := fenceDB(t)
	if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version) VALUES('foreign','other-crew','legacy','stopped',1)`); err != nil {
		t.Fatal(err)
	}
	cfg := provider.CrewConfig{ID: "crew", Slug: "unchanged", Services: []provider.CrewService{{Name: "database"}, {Name: "legacy", Image: "redis:7"}, {Name: "worker"}}}
	filtered, err := FilterServices(t.Context(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.ID != cfg.ID || filtered.Slug != cfg.Slug || len(filtered.Services) != 2 || filtered.Services[0].Name != "legacy" || filtered.Services[0].Image != "redis:7" || filtered.Services[1].Name != "worker" {
		t.Fatalf("filter crossed crew boundary or lost legacy config: %+v", filtered)
	}
	if len(cfg.Services) != 3 || cfg.Services[0].Name != "database" || cfg.Services[1].Name != "legacy" {
		t.Fatalf("filter mutated caller configuration: %+v", cfg.Services)
	}
	// A configuration used by multiple callers cannot share the filtered slice:
	// later adjustments to agent-start services must not alter the source config.
	filtered.Services[0].Name = "adjusted"
	if cfg.Services[1].Name != "legacy" {
		t.Fatal("filtered services alias caller slice")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = FilterServices(t.Context(), db, cfg); err == nil {
		t.Fatal("database outage silently bypassed managed-service authority")
	}
}
