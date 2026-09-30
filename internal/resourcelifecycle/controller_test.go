package resourcelifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if _, err = db.Exec(`CREATE TABLE crews(id TEXT PRIMARY KEY,deleted_at TEXT,workspace_id TEXT NOT NULL DEFAULT 'ws-a');INSERT INTO crews(id,deleted_at) VALUES('deleted','2026-09-30'),('live',NULL)`); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{items: map[string]Container{}}
	c := &Controller{DB: db, InstanceID: "installation-a", BootAt: time.Now().UTC(), Connect: func(context.Context) (Runtime, error) { return f, nil }}
	// The crew DELETE handler records the pending owner.
	c.Pending(context.Background(), "ws-a", "deleted")
	return c, f
}
func item(id, owner, instance string) Container {
	return Container{ID: id, CrewID: owner, InstanceID: instance, Kind: "crew", Mounts: []Mount{{Type: "volume", Name: "anonymous-persist", Destination: "/workspace"}}}
}
func status(t *testing.T, c *Controller) Status {
	t.Helper()
	s, err := c.Statuses(context.Background(), "ws-a")
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

type testDB struct {
	*sql.DB
	location string
}

func migratedDB(t *testing.T) testDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crewship.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	b, err := os.ReadFile("../database/migrations/20260930164209_container_cleanup_diagnostics.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	location, err := DatabaseLocation("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	return testDB{DB: db, location: location}
}

func loadID(t *testing.T, root string, db testDB) string {
	t.Helper()
	identity, err := LoadIdentity(context.Background(), root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { identity.Close() })
	return identity.ID
}

func nonceOf(t *testing.T, db testDB) string {
	t.Helper()
	var nonce string
	if err := db.QueryRow(`SELECT db_nonce FROM resource_cleanup_installation`).Scan(&nonce); err != nil {
		t.Fatal(err)
	}
	return nonce
}

// Dev servers on one host share ~/.crewship; each database must still get its
// own label, or one server's controller treats the others' containers as its own.
func TestInstallationIdentitySharedDataDirDistinctDatabases(t *testing.T) {
	root := t.TempDir()
	a, b := loadID(t, root, migratedDB(t)), loadID(t, root, migratedDB(t))
	if a == b {
		t.Fatal("two databases sharing a data directory got one installation identity")
	}
}

// A copied database carries the original's nonce. In the same data directory,
// once the original stops and releases its lock, the copy must still not take
// the original's identity: it would then remove the original's runtimes.
func TestInstallationIdentityCopiedDatabaseSameDataDir(t *testing.T) {
	root, original := t.TempDir(), migratedDB(t)
	first, err := LoadIdentity(context.Background(), root, original.DB, original.location)
	if err != nil {
		t.Fatal(err)
	}
	originalID, originalNonce := first.ID, nonceOf(t, original)
	first.Close()
	copied := migratedDB(t)
	if _, err := copied.Exec(`INSERT INTO resource_cleanup_installation(id,db_nonce) VALUES(1,?)`, originalNonce); err != nil {
		t.Fatal(err)
	}
	if loadID(t, root, copied) == originalID {
		t.Fatal("database copy in the same data directory took the original's identity")
	}
	if nonceOf(t, copied) == originalNonce {
		t.Fatal("copy was not re-keyed")
	}
	if loadID(t, root, original) != originalID {
		t.Fatal("original lost its identity after the copy started")
	}
}

// A database copied to another data directory must not inherit authority.
func TestInstallationIdentityCopiedDatabaseElsewhereIsNew(t *testing.T) {
	db := migratedDB(t)
	original := loadID(t, t.TempDir(), db)
	if copied := loadID(t, t.TempDir(), db); copied == original {
		t.Fatal("database copy in another data directory inherited the identity")
	}
}

// Two live servers on the same database file: the second must not act on the
// first one's containers, and a restart reclaims the same identity.
func TestInstallationIdentitySecondHolderAndRestart(t *testing.T) {
	root, db := t.TempDir(), migratedDB(t)
	first, err := LoadIdentity(context.Background(), root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(context.Background(), root, db.DB, db.location); !errors.Is(err, ErrIdentityInUse) {
		t.Fatalf("second live holder: %v", err)
	}
	id := first.ID
	first.Close()
	if again := loadID(t, root, db); again != id {
		t.Fatal("restart changed the installation identity")
	}
}

func TestDatabaseLocationCanonical(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	rel, err := DatabaseLocation("file:./crewship.db?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := DatabaseLocation("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil || rel != abs {
		t.Fatalf("relative %q vs absolute %q (%v)", rel, abs, err)
	}
	pg, err := DatabaseLocation("postgresql://user:secret@db.internal:5432/crewship?sslmode=require")
	if err != nil || pg != "postgresql://db.internal:5432/crewship" {
		t.Fatalf("postgres location %q %v", pg, err)
	}
	if _, err := DatabaseLocation("file::memory:?cache=shared"); err == nil {
		t.Fatal("in-memory database accepted as a stable location")
	}
}

func TestInstallationIdentityConcurrentCreateAndInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := readOrCreateID(path)
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
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOrCreateID(path); err == nil {
		t.Fatal("accepted invalid identity")
	}
}

// Every tombstone is an owner, so steady-state ticks must not rewrite them.
func TestSteadyStateTicksWriteOnlyTheScanRow(t *testing.T) {
	c, _ := fixture(t)
	for i := 0; i < 50; i++ {
		if _, err := c.DB.Exec(`INSERT INTO crews(id,deleted_at) VALUES(?, '2026-09-30')`, fmt.Sprintf("old-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	c.Tick(context.Background())
	if _, err := c.DB.Exec(`CREATE TABLE writes(n INTEGER);INSERT INTO writes VALUES(0);
CREATE TRIGGER count_insert AFTER INSERT ON resource_cleanup_status BEGIN UPDATE writes SET n=n+1; END;
CREATE TRIGGER count_update AFTER UPDATE ON resource_cleanup_status BEGIN UPDATE writes SET n=n+1; END;`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		c.Tick(context.Background())
	}
	var n, rows int
	if err := c.DB.QueryRow(`SELECT n FROM writes`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("steady-state status writes %d %v", n, err)
	}
	if err := c.DB.QueryRow(`SELECT count(*) FROM resource_cleanup_status`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("clean tombstones without containers got rows: %d %v", rows, err)
	}
	if s := status(t, c); s.State != "observed_clear" || !s.Complete {
		t.Fatalf("%+v", s)
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
				_, err = c.DB.Exec(`INSERT INTO crews(id,deleted_at) VALUES(NULL,'deleted')`)
			} else {
				_, err = c.DB.Exec(`ALTER TABLE crews RENAME TO owners; CREATE VIEW crews AS SELECT id,deleted_at,workspace_id FROM owners UNION ALL SELECT json_extract('invalid-json','$'),'deleted','ws-a'`)
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
	if _, err := c.DB.Exec(`CREATE TRIGGER deny_clear BEFORE UPDATE ON resource_cleanup_scans WHEN NEW.complete=1 BEGIN SELECT RAISE(FAIL,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	c.Tick(context.Background())
	if s := status(t, c); s.State != "unknown" || s.Complete || s.Error != "diagnostic_write_failed" {
		t.Fatalf("false persisted clear %+v", s)
	}
}

// Status rows are instance-wide; a workspace admin must see only its own crews.
func TestStatusesAreScopedToTheCallersWorkspace(t *testing.T) {
	c, f := fixture(t)
	if _, err := c.DB.Exec(`INSERT INTO crews(id,deleted_at,workspace_id) VALUES('other','2026-09-30','ws-b')`); err != nil {
		t.Fatal(err)
	}
	f.items["b"] = item("b", "other", c.InstanceID)
	f.failStage = "remove"
	c.Tick(context.Background())
	own, err := c.Statuses(context.Background(), "ws-a")
	if err != nil || len(own) != 1 || own[0].CrewID != "deleted" {
		t.Fatalf("ws-a sees %+v %v", own, err)
	}
	other, err := c.Statuses(context.Background(), "ws-b")
	if err != nil || len(other) != 1 || other[0].CrewID != "other" || other[0].Error != "remove_failed" {
		t.Fatalf("ws-b sees %+v %v", other, err)
	}
}

// After a revive the old row is history, not a fresh observation.
func TestRevivedOwnerRowIsNotReportedAsCurrent(t *testing.T) {
	c, f := fixture(t)
	f.items["x"] = item("x", "deleted", c.InstanceID)
	f.failStage = "remove"
	c.Tick(context.Background())
	if s := status(t, c); s.State != "error" || s.Error != "remove_failed" {
		t.Fatalf("setup %+v", s)
	}
	if _, err := c.DB.Exec(`UPDATE crews SET deleted_at=NULL WHERE id='deleted'`); err != nil {
		t.Fatal(err)
	}
	c.Tick(context.Background())
	if s, err := c.Statuses(context.Background(), "ws-a"); err != nil || len(s) != 0 {
		t.Fatalf("revived crew still reported: %+v %v", s, err)
	}
}

// Several starts of the same copied database racing between reading the old
// owner and re-keying must still settle on one identity: one wins, the rest
// meet its lock. Unconditional re-keying forked two identities for one file.
func TestInstallationIdentityConcurrentStartsOfOneCopy(t *testing.T) {
	root, original := t.TempDir(), migratedDB(t)
	first, err := LoadIdentity(context.Background(), root, original.DB, original.location)
	if err != nil {
		t.Fatal(err)
	}
	originalID, originalNonce := first.ID, nonceOf(t, original)
	first.Close()
	copied := migratedDB(t)
	if _, err := copied.Exec(`INSERT INTO resource_cleanup_installation(id,db_nonce) VALUES(1,?)`, originalNonce); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(copied.location, "file:")
	const starts = 3
	var arrived sync.WaitGroup
	arrived.Add(starts)
	var once sync.Map
	testHookBeforeRekey = func() {
		// Hold every start here once, so all of them read the old owner.
		if _, seen := once.LoadOrStore(goroutineKey(), true); !seen {
			arrived.Done()
			arrived.Wait()
		}
	}
	t.Cleanup(func() { testHookBeforeRekey = nil })
	type result struct {
		identity *Identity
		err      error
	}
	results := make(chan result, starts)
	for i := 0; i < starts; i++ {
		go func() {
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
			if err != nil {
				results <- result{err: err}
				return
			}
			t.Cleanup(func() { db.Close() })
			identity, err := LoadIdentity(context.Background(), root, db, copied.location)
			results <- result{identity, err}
		}()
	}
	var held []*Identity
	for i := 0; i < starts; i++ {
		r := <-results
		switch {
		case r.err == nil:
			held = append(held, r.identity)
			t.Cleanup(func() { r.identity.Close() })
		case !errors.Is(r.err, ErrIdentityInUse):
			t.Fatalf("start failed: %v", r.err)
		}
	}
	if len(held) != 1 {
		t.Fatalf("%d live identities for one database file", len(held))
	}
	if held[0].ID == originalID {
		t.Fatal("copy took the original's identity")
	}
}

// goroutineKey identifies the calling goroutine for the test hook.
func goroutineKey() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	return strings.Fields(string(buf))[1]
}

// A symlink to the live database file is the same database: the second start
// must meet the live holder's lock, not re-key the original away from it.
func TestInstallationIdentitySymlinkedDatabaseIsTheSameDatabase(t *testing.T) {
	root, db := t.TempDir(), migratedDB(t)
	holder, err := LoadIdentity(context.Background(), root, db.DB, db.location)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Close() })
	link := filepath.Join(t.TempDir(), "linked.db")
	if err := os.Symlink(strings.TrimPrefix(db.location, "file:"), link); err != nil {
		t.Fatal(err)
	}
	location, err := DatabaseLocation("file:" + link)
	if err != nil {
		t.Fatal(err)
	}
	if location != db.location {
		t.Fatalf("symlink resolved to %q, want %q", location, db.location)
	}
	other, err := sql.Open("sqlite", "file:"+link)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if _, err := LoadIdentity(context.Background(), root, other, location); !errors.Is(err, ErrIdentityInUse) {
		t.Fatalf("symlinked second start: %v", err)
	}
}

func TestDatabaseLocationRejectsNamedMemory(t *testing.T) {
	for _, u := range []string{"file:name?mode=memory&cache=shared", "file:x.db?vfs=memdb", "file::memory:", ":memory:"} {
		if _, err := DatabaseLocation(u); err == nil {
			t.Errorf("%s accepted as a stable location", u)
		}
	}
}
