package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

type managedServiceRuntime struct {
	provider.ContainerProvider
	mu                    sync.Mutex
	starts, stops, agents int
	entered, release      chan struct{}
}

func (f *managedServiceRuntime) EnsureCrewServices(ctx context.Context, cfg provider.CrewConfig) (map[string]string, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return map[string]string{cfg.Services[0].Name: "container"}, nil
}
func (f *managedServiceRuntime) StopCrewService(context.Context, string, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}
func (f *managedServiceRuntime) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents++
	return "agent-container", nil
}
func (f *managedServiceRuntime) StopCrewServices(context.Context, string, string) error   { return nil }
func (f *managedServiceRuntime) RemoveCrewServices(context.Context, string, string) error { return nil }

func TestManagedServices_DurableStateAndAuthority(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewWithServices(t, db, "managed", ws, "managed")
	runtime := &managedServiceRuntime{}
	h := NewCrewHandler(db, newTestLogger())
	h.SetContainer(runtime)
	update := func(role, workspace, state string, version int, want int) {
		t.Helper()
		r := withWorkspaceUser(httptest.NewRequest("PUT", "/", strings.NewReader(fmt.Sprintf(`{"desired_state":%q,"expected_version":%d}`, state, version))), user, workspace, role)
		r.SetPathValue("crewId", "managed")
		r.SetPathValue("serviceName", "redis")
		w := httptest.NewRecorder()
		h.SetServiceState(w, r)
		if w.Code != want {
			t.Fatalf("state update %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	update("MEMBER", ws, "running", 0, 403)
	update("MANAGER", "foreign", "running", 0, 404)
	update("MANAGER", ws, "running", 0, 202)
	update("MANAGER", ws, "stopped", 0, 409)
	resolver := func(ctx context.Context, c, w, n string) (provider.CrewConfig, error) {
		cfg, err := ResolveManagedService(ctx, db, c, w, n)
		if err == nil && (len(cfg.Services) != 1 || !cfg.Services[0].ControllerManaged) {
			return provider.CrewConfig{}, fmt.Errorf("managed service permits Docker to bypass durable intent")
		}
		return cfg, err
	}
	c := &servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: resolver}
	c.Reconcile(context.Background())
	if runtime.starts != 1 || runtime.agents != 0 {
		t.Fatal("service start woke an agent or did not start")
	}
	// A new controller after process restart reconstructs intent from the DB.
	execOrFatal(t, db, `UPDATE service_runtime_intents SET next_attempt_at=''`)
	(&servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: resolver}).Reconcile(context.Background())
	if runtime.starts != 2 {
		t.Fatal("running intent was not restored")
	}
	update("MANAGER", ws, "stopped", 1, 202)
	c.Reconcile(context.Background())
	if runtime.stops != 1 {
		t.Fatal("manual stop not applied")
	}
	// Even a caller supplying its own full config cannot resurrect the service.
	cfg, err := BuildCrewRuntimeConfig(context.Background(), db, "managed", ws)
	if err != nil {
		t.Fatal(err)
	}
	_, err = crewstart.New(runtime, NewCrewConfigCompleter(db), newTestLogger()).Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.starts != 2 {
		t.Fatal("agent start resurrected stopped service")
	}
	execOrFatal(t, db, `UPDATE service_runtime_intents SET next_attempt_at=''`)
	(&servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: resolver}).Reconcile(context.Background())
	if runtime.starts != 2 || runtime.stops != 2 {
		t.Fatal("stop did not survive controller restart")
	}
}

func TestManagedServices_LeasePreventsTwoControllers(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewWithServices(t, db, "managed", ws, "managed")
	execOrFatal(t, db, `INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,updated_at) VALUES ('intent','managed','redis','running','')`)
	runtime := &managedServiceRuntime{entered: make(chan struct{}, 2), release: make(chan struct{})}
	resolver := func(ctx context.Context, c, w, n string) (provider.CrewConfig, error) {
		return ResolveManagedService(ctx, db, c, w, n)
	}
	c := &servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: resolver}
	done := make(chan struct{})
	go func() { defer close(done); c.Reconcile(context.Background()) }()
	<-runtime.entered
	(&servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: resolver}).Reconcile(context.Background())
	close(runtime.release)
	<-done
	if runtime.starts != 1 {
		t.Fatal("two controllers executed one intent")
	}
}

func TestManagedServices_MissingCredentialBlocksOnlyItsService(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewWithServices(t, db, "managed", ws, "managed")
	execOrFatal(t, db, `UPDATE crews SET services_json='[{"name":"broken","image":"x","env_refs":["MISSING"]},{"name":"ok","image":"x"}]' WHERE id='managed'`)
	if _, err := ResolveManagedService(context.Background(), db, "managed", ws, "broken"); err == nil {
		t.Fatal("missing credential allowed startup")
	}
	if _, err := ResolveManagedService(context.Background(), db, "managed", ws, "ok"); err != nil {
		t.Fatal("unrelated secret blocked service", err)
	}
}

func TestManagedServices_RevokedCredentialStopsExistingProcess(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCrewWithServices(t, db, "managed", ws, "managed")
	execOrFatal(t, db, `INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,observed_state,updated_at) VALUES ('intent','managed','redis','running','running','')`)
	runtime := &managedServiceRuntime{}
	c := &servicelifecycle.Controller{DB: db, Runtime: runtime, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, fmt.Errorf("synthetic secret must not appear in status")
	}}
	c.Reconcile(context.Background())
	if runtime.stops != 1 || runtime.starts != 0 {
		t.Fatal("revocation left existing process running")
	}
	var state, code string
	if err := db.QueryRow(`SELECT observed_state,last_error FROM service_runtime_intents`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "error" || code != "configuration_or_credentials_unavailable" {
		t.Fatalf("unsafe status %q %q", state, code)
	}
	cfg, err := servicelifecycle.FilterServices(context.Background(), db, provider.CrewConfig{ID: "managed", Services: []provider.CrewService{{Name: "redis"}, {Name: "legacy"}}})
	if err != nil || len(cfg.Services) != 1 || cfg.Services[0].Name != "legacy" {
		t.Fatalf("agent startup bypasses managed authority: %+v %v", cfg, err)
	}
}
