package servicelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestMaintenanceRejectsInvalidAdmissionAndStorageFailure(t *testing.T) {
	db, _ := fenceDB(t)
	for _, gate := range []func(context.Context, *sql.DB, string, string) (string, error){BeginBackupFence, AdoptBackupFence} {
		for _, input := range []struct {
			db              *sql.DB
			crew, operation string
		}{{nil, "crew", "backup"}, {db, "", "backup"}, {db, "crew", "unknown"}} {
			if token, err := gate(t.Context(), input.db, input.crew, input.operation); token != "" || !errors.Is(err, ErrBackupMaintenance) {
				t.Fatalf("invalid admission: %q %v", token, err)
			}
		}
	}
	for _, input := range []struct {
		db   *sql.DB
		crew string
	}{{nil, "crew"}, {db, ""}} {
		if release, err := ServiceOperations(input.db)(t.Context(), input.crew); release != nil || !errors.Is(err, ErrBackupMaintenance) {
			t.Fatalf("invalid operation admitted: %v", err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_operation BEFORE INSERT ON service_operation_leases BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if release, err := ServiceOperations(db)(t.Context(), "crew"); release != nil || err == nil {
		t.Fatal("failed operation write admitted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []func(context.Context, *sql.DB, string, string) (string, error){BeginBackupFence, AdoptBackupFence} {
		if token, err := gate(t.Context(), db, "crew", "backup"); token != "" || err == nil || errors.Is(err, ErrBackupMaintenance) {
			t.Fatalf("storage outage disguised as maintenance: %q %v", token, err)
		}
	}
	for _, end := range []func(context.Context, *sql.DB, string, string) error{EndBackupFence, RenewBackupFence} {
		if err := end(t.Context(), db, "crew", "token"); err == nil {
			t.Fatal("storage outage ignored")
		}
	}
	if release, err := ServiceOperations(db)(t.Context(), "crew"); release != nil || err == nil {
		t.Fatal("closed storage admitted an operation")
	}
}

func TestControllerUnavailableStateNeverCallsRuntime(t *testing.T) {
	for _, scenario := range []string{"closed database", "malformed intent", "claim failure", "stale version"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := fenceDB(t)
			runtime := &fencedRuntime{}
			c := Controller{DB: db, Runtime: runtime, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
				t.Error("unavailable intent resolved")
				return provider.CrewConfig{}, nil
			}}
			switch scenario {
			case "closed database":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				c.Reconcile(t.Context())
				c.reconcileOne(t.Context(), intent{id: "intent", version: 4})
			case "malformed intent":
				if _, err := db.Exec(`UPDATE service_runtime_intents SET version='malformed'`); err != nil {
					t.Fatal(err)
				}
				c.Reconcile(t.Context())
			case "claim failure":
				if _, err := db.Exec(`CREATE TRIGGER reject_claim BEFORE UPDATE ON service_runtime_intents BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
				c.Reconcile(t.Context())
			case "stale version":
				c.reconcileOne(t.Context(), intent{id: "intent", version: 3})
			}
			if runtime.calls != 0 {
				t.Fatal("runtime called without authoritative lease")
			}
		})
	}
}

func TestControllerStopsRunOnCancellationAndToleratesMissingDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	go func() { defer close(done); (&Controller{}).Run(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled controller did not exit")
	}
	if err := StopCrew(t.Context(), nil, "crew"); err != nil {
		t.Fatal(err)
	}
	cfg := provider.CrewConfig{ID: "crew", Services: []provider.CrewService{{Name: "database"}}}
	if got, err := FilterServices(t.Context(), nil, cfg); err != nil || len(got.Services) != 1 {
		t.Fatalf("nil-store filter changed config: %+v %v", got, err)
	}
}

func TestControllerCompletionStorageFailureRetainsLease(t *testing.T) {
	db, _ := fenceDB(t)
	runtime := lifecycleRuntime{ensure: func(context.Context, provider.CrewConfig) (map[string]string, error) {
		_, err := db.Exec(`CREATE TRIGGER reject_completion BEFORE UPDATE ON service_runtime_intents BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`)
		return nil, err
	}, stop: func(context.Context, string, string, string) error { return nil }}
	c := Controller{DB: db, Runtime: runtime, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, nil
	}}
	c.Reconcile(t.Context())
	var observed, owner string
	if err := db.QueryRow(`SELECT observed_state,lease_owner FROM service_runtime_intents`).Scan(&observed, &owner); err != nil {
		t.Fatal(err)
	}
	if observed != "" || owner == "" {
		t.Fatalf("failed durable completion reported success or lost exclusion: %q %q", observed, owner)
	}
}
