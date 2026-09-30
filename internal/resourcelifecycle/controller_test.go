package resourcelifecycle

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
)

type fakeRuntime struct {
	items         map[string]Container
	calls         []string
	lists         int
	failList      int
	failStage     string
	beforeInspect func()
}

func (f *fakeRuntime) List(context.Context) ([]Container, error) {
	f.lists++
	if f.failList == f.lists {
		return nil, errors.New("secret provider error")
	}
	out := []Container{}
	for _, x := range f.items {
		out = append(out, x)
	}
	return out, nil
}
func (f *fakeRuntime) Inspect(_ context.Context, id string) (Container, error) {
	if f.beforeInspect != nil {
		f.beforeInspect()
		f.beforeInspect = nil
	}
	if f.failStage == "inspect" {
		return Container{}, errors.New("secret")
	}
	x, ok := f.items[id]
	if !ok {
		return Container{}, ErrNotFound
	}
	return x, nil
}
func (f *fakeRuntime) Stop(_ context.Context, id string) error {
	f.calls = append(f.calls, "stop:"+id)
	if f.failStage == "stop" {
		return errors.New("secret")
	}
	return nil
}
func (f *fakeRuntime) Remove(_ context.Context, id string) error {
	f.calls = append(f.calls, "remove:"+id)
	if f.failStage == "remove" {
		return errors.New("secret")
	}
	delete(f.items, id)
	return nil
}
func (f *fakeRuntime) Close() error { return nil }
func fixture(t *testing.T) (*Controller, *fakeRuntime) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	b, err := os.ReadFile("../database/migrations/20260930164209_container_cleanup_diagnostics.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE crews(id TEXT PRIMARY KEY,deleted_at TEXT);INSERT INTO crews VALUES('deleted','2026-09-30'),('live',NULL)`); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{items: map[string]Container{}}
	c := &Controller{DB: db, InstanceID: "installation-a", BootAt: time.Now().UTC(), Connect: func(context.Context) (Runtime, error) { return f, nil }}
	return c, f
}
func item(id, owner, instance string) Container {
	return Container{ID: id, CrewID: owner, InstanceID: instance, Kind: "crew", Mounts: []Mount{{Type: "volume", Name: "anonymous-persist", Destination: "/workspace"}}}
}
func status(t *testing.T, c *Controller) Status {
	t.Helper()
	s, err := c.Statuses(context.Background())
	if err != nil || len(s) != 1 {
		t.Fatalf("statuses %v %v", s, err)
	}
	return s[0]
}
func TestDeletedOnlyPreservesEvidence(t *testing.T) {
	c, f := fixture(t)
	for _, x := range []Container{item("runtime", "deleted", c.InstanceID), item("stopped", "deleted", c.InstanceID), item("live", "live", c.InstanceID), item("foreign", "deleted", "installation-b"), item("legacy", "deleted", ""), item("missing", "absent", c.InstanceID)} {
		f.items[x.ID] = x
	}
	sidecar := item("service", "deleted", c.InstanceID)
	sidecar.Kind = "sidecar"
	f.items[sidecar.ID] = sidecar
	c.Tick(context.Background())
	if len(f.items) != 4 {
		t.Fatalf("remaining %+v", f.items)
	}
	for _, id := range []string{"live", "foreign", "legacy", "missing"} {
		if _, ok := f.items[id]; !ok {
			t.Fatalf("removed %s", id)
		}
	}
	s := status(t, c)
	if s.State != "observed_clear" || !s.Complete || s.Remaining != 0 || s.Unattributed != 1 {
		t.Fatalf("status %+v", s)
	}
	var n int
	if err := c.DB.QueryRow(`SELECT count(*) FROM resource_cleanup_mounts WHERE mounts_json LIKE '%anonymous-persist%'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("mount evidence %d %v", n, err)
	}
}
func TestBootUnavailableAndLateCreateRetry(t *testing.T) {
	c, f := fixture(t)
	connect := c.Connect
	c.Connect = func(context.Context) (Runtime, error) { return nil, errors.New("credential://secret") }
	c.Tick(context.Background())
	if s := status(t, c); s.State != "unknown" || s.Error != "provider_unavailable" || s.Complete {
		t.Fatalf("%+v", s)
	}
	c.Connect = connect
	f.items["late"] = item("late", "deleted", c.InstanceID)
	c.Tick(context.Background())
	if s := status(t, c); s.State != "observed_clear" {
		t.Fatalf("%+v", s)
	}
	f.items["later"] = item("later", "deleted", c.InstanceID)
	c.Tick(context.Background())
	if len(f.items) != 0 {
		t.Fatal("late create leaked")
	}
}
func TestFailuresNeverClear(t *testing.T) {
	for _, stage := range []string{"inspect", "stop", "remove", "first_scan", "final_scan", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			c, f := fixture(t)
			f.items["x"] = item("x", "deleted", c.InstanceID)
			switch stage {
			case "first_scan":
				f.failList = 1
			case "final_scan":
				f.failList = 2
			case "snapshot":
				if _, err := c.DB.Exec(`DROP TABLE resource_cleanup_mounts`); err != nil {
					t.Fatal(err)
				}
			default:
				f.failStage = stage
			}
			c.Tick(context.Background())
			s := status(t, c)
			if s.State == "observed_clear" || s.Error == "" {
				t.Fatalf("%+v", s)
			}
			if stage == "snapshot" && len(f.calls) != 0 {
				t.Fatal("destruction before durable mount evidence")
			}
		})
	}
}
func TestFullScanWithRemovalCapAndRestart(t *testing.T) {
	c, f := fixture(t)
	c.Batch = 1
	f.items["a"] = item("a", "deleted", c.InstanceID)
	f.items["b"] = item("b", "deleted", c.InstanceID)
	c.Tick(context.Background())
	if s := status(t, c); s.State != "pending" || s.Remaining != 1 || !s.Complete {
		t.Fatalf("%+v", s)
	}
	c.BootAt = time.Now().UTC().Add(time.Second)
	if s := status(t, c); s.State != "unknown" || s.Complete {
		t.Fatalf("restart %+v", s)
	}
	c.BootAt = time.Now().UTC()
	c.Tick(context.Background())
	if s := status(t, c); s.State != "observed_clear" {
		t.Fatalf("%+v", s)
	}
}
func TestOwnerReviveAndImmutableIdentityRecheck(t *testing.T) {
	for _, mode := range []string{"revive", "foreign", "absent", "notfound"} {
		t.Run(mode, func(t *testing.T) {
			c, f := fixture(t)
			f.items["x"] = item("x", "deleted", c.InstanceID)
			f.beforeInspect = func() {
				switch mode {
				case "revive":
					_, err := c.DB.Exec(`UPDATE crews SET deleted_at=NULL`)
					if err != nil {
						t.Fatal(err)
					}
				case "foreign":
					f.items["x"] = item("x", "deleted", "other")
				case "absent":
					_, err := c.DB.Exec(`DELETE FROM crews`)
					if err != nil {
						t.Fatal(err)
					}
				case "notfound":
					delete(f.items, "x")
				}
			}
			c.Tick(context.Background())
			if len(f.calls) != 0 {
				t.Fatalf("unsafe calls %+v", f.calls)
			}
		})
	}
}
func TestInstallationIdentityConcurrentAndCopyIndependent(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := LoadIdentity(root)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	expected := ""
	for id := range ids {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Fatal("startup identity race")
		}
	}
	second, err := LoadIdentity(t.TempDir())
	if err != nil || second == expected {
		t.Fatal("installation copy collision")
	}
	if err := os.WriteFile(filepath.Join(root, "instance-id"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(root); err == nil {
		t.Fatal("accepted invalid identity")
	}
}

func TestOwnerInventoryIterationFailureInvalidatesPriorClear(t *testing.T) {
	for _, mode := range []string{"scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := fixture(t)
			c.Tick(context.Background())
			if s := status(t, c); s.State != "observed_clear" {
				t.Fatal(s)
			}
			var err error
			if mode == "scan" {
				_, err = c.DB.Exec(`INSERT INTO crews VALUES(NULL,'deleted')`)
			} else {
				_, err = c.DB.Exec(`ALTER TABLE crews RENAME TO owners; CREATE VIEW crews AS SELECT id,deleted_at FROM owners UNION ALL SELECT json_extract('invalid-json','$'),'deleted'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			c.Tick(context.Background())
			if s := status(t, c); s.State != "unknown" || s.Complete || s.Error != "owner_inventory_failed" {
				t.Fatalf("false clear after %s failure: %+v", mode, s)
			}
		})
	}
}

func TestFinalDiagnosticWriteFailureInvalidatesPriorClear(t *testing.T) {
	c, _ := fixture(t)
	c.Tick(context.Background())
	if _, err := c.DB.Exec(`CREATE TRIGGER deny_clear BEFORE UPDATE ON resource_cleanup_status WHEN NEW.state='observed_clear' BEGIN SELECT RAISE(FAIL,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	c.Tick(context.Background())
	if s := status(t, c); s.State != "unknown" || s.Complete || s.Error != "diagnostic_write_failed" {
		t.Fatalf("false persisted clear %+v", s)
	}
}
