package servicelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func fenceDB(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fence.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return db
	}
	a, b := open(), open()
	_, err := a.Exec(`CREATE TABLE workspaces(id TEXT PRIMARY KEY);
 CREATE TABLE crews(id TEXT PRIMARY KEY,workspace_id TEXT,slug TEXT,services_json TEXT,deleted_at TEXT);
 CREATE TABLE service_runtime_intents(id TEXT PRIMARY KEY,crew_id TEXT,service_name TEXT,desired_state TEXT,version INTEGER,lease_owner TEXT DEFAULT '',lease_until TEXT DEFAULT '',next_attempt_at TEXT DEFAULT '',observed_state TEXT DEFAULT '',last_error TEXT DEFAULT '',updated_at TEXT DEFAULT '');
 INSERT INTO crews VALUES('crew','workspace','crew','[]',NULL);
 INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version) VALUES('intent','crew','database','running',4);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Exec(`CREATE TABLE service_operation_leases(token TEXT PRIMARY KEY,crew_id TEXT,workspace_id TEXT,lease_until TEXT)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../database/migrations/20261001160000_service_backup_fences.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Exec(`ALTER TABLE service_backup_fences ADD COLUMN producer_until TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	return a, b
}

type fencedRuntime struct{ calls int }

func (r *fencedRuntime) EnsureCrewServices(context.Context, provider.CrewConfig) (map[string]string, error) {
	r.calls++
	return nil, nil
}
func (r *fencedRuntime) StopCrewService(context.Context, string, string, string) error {
	r.calls++
	return nil
}

func TestBackupFenceSurvivesReconcileAndBlocksMutation(t *testing.T) {
	a, b := fenceDB(t)
	ctx := context.Background()
	token, err := BeginBackupFence(ctx, a, "crew", "backup")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fencedRuntime{}
	controller := &Controller{DB: b, Runtime: runtime, Resolve: func(context.Context, string, string, string) (provider.CrewConfig, error) {
		return provider.CrewConfig{}, nil
	}}
	controller.Reconcile(ctx)
	if runtime.calls != 0 {
		t.Fatal("maintenance restarted application")
	}
	for _, query := range []string{
		`UPDATE service_runtime_intents SET desired_state='stopped',version=version+1`,
		`DELETE FROM service_runtime_intents`,
		`UPDATE crews SET services_json='[{}]'`,
		`UPDATE crews SET deleted_at='now'`,
		`DELETE FROM crews`,
	} {
		if _, err = b.Exec(query); err == nil {
			t.Fatalf("maintenance allowed %s", query)
		}
	}
	if err = EndBackupFence(ctx, b, "crew", "wrong"); err == nil {
		t.Fatal("wrong token released fence")
	}
	if err = EndBackupFence(ctx, b, "crew", token); err != nil {
		t.Fatal(err)
	}
	controller.Reconcile(ctx)
	if runtime.calls != 1 {
		t.Fatalf("expected preserved running intent, calls=%d", runtime.calls)
	}
	var state string
	var version int64
	if err = a.QueryRow(`SELECT desired_state,version FROM service_runtime_intents`).Scan(&state, &version); err != nil || state != "running" || version != 4 {
		t.Fatalf("intent changed: %s %d %v", state, version, err)
	}
}

func TestBackupFenceAndControllerLeaseAreMutuallyExclusive(t *testing.T) {
	a, b := fenceDB(t)
	ctx := context.Background()
	for trial := 0; trial < 20; trial++ {
		_, err := a.Exec(`DELETE FROM service_backup_fences;UPDATE service_runtime_intents SET lease_until='',lease_owner=''`)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var token string
		var fenceErr, leaseErr error
		var claimed int64
		go func() { defer wg.Done(); <-start; token, fenceErr = BeginBackupFence(ctx, a, "crew", "backup") }()
		go func() {
			defer wg.Done()
			<-start
			res, err := b.Exec(`UPDATE service_runtime_intents SET lease_owner='controller',lease_until=? WHERE id='intent' AND lease_until='' AND NOT EXISTS(SELECT 1 FROM service_backup_fences WHERE crew_id='crew')`, tsformat.Format(time.Now().Add(time.Minute)))
			leaseErr = err
			if err == nil {
				claimed, _ = res.RowsAffected()
			}
		}()
		close(start)
		wg.Wait()
		if leaseErr != nil {
			t.Fatal(leaseErr)
		}
		if (fenceErr == nil) == (claimed == 1) {
			t.Fatalf("claim conservation violated: fence=%v lease=%d token=%s", fenceErr, claimed, token)
		}
	}
}

func TestBackupFenceDrainsLegacyServiceOperation(t *testing.T) {
	a, b := fenceDB(t)
	ctx := context.Background()
	release, err := ServiceOperations(a)(ctx, "crew")
	if err != nil {
		t.Fatal(err)
	}
	// An operation paused after admission still prevents capture on another DB
	// connection; maintenance cannot rely on an in-process provider mutex.
	if _, err = BeginBackupFence(ctx, b, "crew", "backup"); err == nil {
		t.Fatal("backup admitted an in-flight legacy start")
	}
	if err = release(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := BeginBackupFence(ctx, b, "crew", "backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ServiceOperations(a)(ctx, "crew"); err == nil {
		t.Fatal("legacy start bypassed durable maintenance")
	}
	if err = EndBackupFence(ctx, a, "crew", token); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFenceAndLegacyServiceAdmissionAreAtomic(t *testing.T) {
	a, b := fenceDB(t)
	ctx := context.Background()
	for trial := 0; trial < 20; trial++ {
		if _, err := a.Exec(`DELETE FROM service_backup_fences;DELETE FROM service_operation_leases`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var fenceErr, opErr error
		go func() { defer wg.Done(); <-start; _, fenceErr = BeginBackupFence(ctx, a, "crew", "backup") }()
		go func() { defer wg.Done(); <-start; _, opErr = ServiceOperations(b)(ctx, "crew") }()
		close(start)
		wg.Wait()
		if (fenceErr == nil) == (opErr == nil) {
			t.Fatalf("maintenance/start conservation failed: fence=%v operation=%v", fenceErr, opErr)
		}
	}
}

func TestBeginBackupFencePreservesDatabaseErrors(t *testing.T) {
	db, _ := fenceDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := BeginBackupFence(ctx, db, "crew", "backup"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation misclassified: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE service_backup_fences`); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginBackupFence(t.Context(), db, "crew", "backup"); err == nil || errors.Is(err, ErrBackupMaintenance) {
		t.Fatalf("database failure misclassified: %v", err)
	}
}
