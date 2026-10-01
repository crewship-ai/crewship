//go:build linux && quota_live

package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider"
	providerdocker "github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/quota"
	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

type snapshotLiveHost struct {
	db      *sql.DB
	catalog quota.Client
	backend *quota.Backend
	runtime *providerdocker.Provider
}

func newSnapshotLiveHost(t *testing.T, ctx context.Context, namespace string) *snapshotLiveHost {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "catalog"), 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := quota.NewBackend(filepath.Join(root, "catalog"), 256<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.BindNamespace(namespace); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "helper.sock")
	serverCtx, stop := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- quota.ServeNamespace(serverCtx, socket, 0, backend, namespace, func() error { close(ready); return nil })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	catalog := quota.Client{Socket: socket, Namespace: namespace}
	runtime, err := providerdocker.New(ctx, providerdocker.Config{ContainerPrefix: namespace, InstanceID: namespace, QuotaCatalog: catalog}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	db := openMigratedDBCov(t)
	runtime.SetServiceOperationGate(servicelifecycle.ServiceOperations(db))
	t.Cleanup(func() { _ = runtime.Close(); stop(); <-done; _ = backend.Close() })
	return &snapshotLiveHost{db: db, catalog: catalog, backend: backend, runtime: runtime}
}

type interruptedSnapshotTransport struct {
	ServiceSnapshotRuntime
	failed bool
}

func (p *interruptedSnapshotTransport) ExportQuotaVolume(ctx context.Context, key quota.Key, size int64, w io.Writer) error {
	if err := p.ServiceSnapshotRuntime.ExportQuotaVolume(ctx, key, size, w); err != nil {
		return err
	}
	if !p.failed {
		p.failed = true
		return fmt.Errorf("synthetic producer interruption after verified export")
	}
	return nil
}

func TestLiveEncryptedQuotaBackupRestoresTwoOwnersFreshGenerations(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("CREWSHIP_LIVE_QUOTA_BACKUP") != "1" {
		t.Fatal("requires explicitly selected owned root Docker/quota VM fixture")
	}
	var encryptionKey [32]byte
	if _, err := rand.Read(encryptionKey[:]); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(encryptionKey[:]))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	image := os.Getenv("CREWSHIP_QUOTA_BACKUP_FIXTURE_IMAGE")
	if len(image) != 71 || image[:7] != "sha256:" {
		t.Fatal("requires installed immutable synthetic fixture image ID")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	source := newSnapshotLiveHost(t, ctx, "quota-backup-source-"+suffix)
	destination := newSnapshotLiveHost(t, ctx, "quota-backup-target-"+suffix)
	workspace, firstCrew := seedCovWorkspace(t, source.db, "snapshot_"+suffix)
	secondCrew := "second-" + suffix
	if _, err := source.db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, secondCrew, workspace, "Second", "second-"+suffix); err != nil {
		t.Fatal(err)
	}
	crews := []string{firstCrew, secondCrew}
	users := []string{"u_cov_snapshot_" + suffix, "second-user-" + suffix}
	agents := []string{"a_cov_snapshot_" + suffix, "second-agent-" + suffix}
	if _, err := source.db.Exec(`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, users[1], "second-"+suffix+"@synthetic.test", "Second owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,status) VALUES(?,?,?,'Second','second','IDLE')`, agents[1], workspace, secondCrew); err != nil {
		t.Fatal(err)
	}
	for i, crew := range crews {
		member := "member-" + crew
		if _, err := source.db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, member, workspace, users[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := source.db.Exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES(?,?,?)`, "crew-member-"+crew, crew, users[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := source.db.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES(?,?,'agent',?,'run',?,'2026-09-30T00:00:00Z')`, "grant-"+crew, member, agents[i], users[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i, crew := range crews {
		body := fmt.Sprintf(`[{"name":"database","image":%q,"command":["sh","-c","sleep 600"],"quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":7}]}]`, image)
		if _, err := source.db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
			t.Fatal(err)
		}
		if _, err := source.db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES(?,?,'database','running',7,'2026-09-30T00:00:00Z')`, "intent-"+crew, crew); err != nil {
			t.Fatal(err)
		}
		key := quota.Key{Crew: crew, Service: "database", Volume: "data", Generation: 7}
		descriptor, err := source.catalog.Ensure(ctx, key, quota.MinBytes, quota.Owner{UID: 1001, GID: 1002})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.catalog.Remove(context.Background(), key) })
		path := filepath.Join(descriptor.Mount, "owner-canary")
		if err = os.WriteFile(path, []byte(fmt.Sprintf("PRIVATE_OWNER_%d", i)), 0640); err != nil {
			t.Fatal(err)
		}
		if err = os.Chown(path, 1001, 1002); err != nil {
			t.Fatal(err)
		}
	}
	passphrase := "synthetic-disposable-backup-passphrase"
	interrupted := &interruptedSnapshotTransport{ServiceSnapshotRuntime: source.runtime}
	failedOptions := CreateOptions{Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: passphrase, Actor: covAdminActor(), ServiceSnapshots: interrupted}
	if _, err := CreateBackup(ctx, source.db, failedOptions); err == nil {
		t.Fatal("interrupted export advertised completed archive")
	}
	maintenance, err := ServiceMaintenanceStatus(ctx, source.db, workspace)
	if err != nil || len(maintenance) != 1 {
		t.Fatalf("interruption lost maintenance: %+v %v", maintenance, err)
	}
	if _, err := source.db.Exec(`UPDATE crews SET deleted_at='2026-09-30T00:00:00Z' WHERE id=?`, maintenance[0].CrewID); err == nil {
		t.Fatal("soft deletion bypassed maintenance")
	}
	if _, err := CreateBackup(ctx, source.db, failedOptions); err == nil {
		t.Fatal("ordinary backup adopted unknown maintenance")
	}
	failedOptions.RecoverServiceMaintenance = true
	if _, err := CreateBackup(ctx, source.db, failedOptions); err == nil {
		t.Fatal("recovery stole live producer lease")
	}
	// Simulate the elapsed liveness interval only after the producer returned.
	if _, err := source.db.Exec(`UPDATE service_backup_fences SET producer_until='2000-01-01T00:00:00Z' WHERE workspace_id=?`, workspace); err != nil {
		t.Fatal(err)
	}
	result, err := CreateBackup(ctx, source.db, CreateOptions{Scope: ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: passphrase, Actor: covAdminActor(), ServiceSnapshots: source.runtime, RecoverServiceMaintenance: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Manifest.Encryption.Enabled || result.Manifest.Contents.ServiceSnapshots != 2 {
		t.Fatal("encrypted physical snapshots missing")
	}
	restored, err := RestoreBackup(ctx, destination.db, RestoreOptions{Path: result.Path, Passphrase: passphrase, Actor: covAdminActor(), ServiceSnapshots: destination.runtime})
	if err != nil {
		t.Fatal(err)
	}
	if restored.RestoredWorkspaceID != workspace {
		t.Fatalf("owner workspace changed: %+v", restored)
	}
	for i, crew := range crews {
		var ownMembership, ownGrant, foreignGrant int
		if err := destination.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE crew_id=? AND user_id=?`, crew, users[i]).Scan(&ownMembership); err != nil {
			t.Fatal(err)
		}
		if err := destination.db.QueryRow(`SELECT COUNT(*) FROM access_grants g JOIN workspace_members m ON m.id=g.member_id WHERE m.user_id=? AND m.workspace_id=? AND g.agent_id=? AND g.operation='run'`, users[i], workspace, agents[i]).Scan(&ownGrant); err != nil {
			t.Fatal(err)
		}
		if err := destination.db.QueryRow(`SELECT COUNT(*) FROM access_grants g JOIN workspace_members m ON m.id=g.member_id WHERE m.user_id=? AND g.agent_id=?`, users[1-i], agents[i]).Scan(&foreignGrant); err != nil {
			t.Fatal(err)
		}
		if ownMembership != 1 || ownGrant != 1 || foreignGrant != 0 {
			t.Fatal("restored membership/grants changed audience")
		}
		var body, state string
		var version int64
		if err = destination.db.QueryRow(`SELECT services_json FROM crews WHERE id=? AND workspace_id=?`, crew, workspace).Scan(&body); err != nil {
			t.Fatal(err)
		}
		specs, err := declaredServiceSnapshots(body, crew, "unused")
		if err != nil || len(specs) != 1 {
			t.Fatal(err)
		}
		key := specs[0].key()
		if key.Generation == 7 || key.Crew != crew {
			t.Fatal("archive overwrote source generation/owner")
		}
		t.Cleanup(func() { _ = destination.catalog.Remove(context.Background(), key) })
		descriptor, err := destination.catalog.Verify(ctx, key, quota.MinBytes)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(descriptor.Mount, "owner-canary")
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != fmt.Sprintf("PRIVATE_OWNER_%d", i) {
			t.Fatalf("owner data mismatch: %q %v", raw, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if info.Mode().Perm() != 0640 || stat.Uid != 1001 || stat.Gid != 1002 {
			t.Fatal("filesystem ownership/mode lost")
		}
		if err = destination.db.QueryRow(`SELECT desired_state,version FROM service_runtime_intents WHERE crew_id=? AND service_name='database'`, crew).Scan(&state, &version); err != nil || state != "running" || version != 7 {
			t.Fatalf("intent changed: %s %d %v", state, version, err)
		}
	}
	maintenance, err = ServiceMaintenanceStatus(ctx, destination.db, workspace)
	if err != nil || len(maintenance) != 0 {
		t.Fatalf("completed imports did not resume intents: %+v %v", maintenance, err)
	}
	controller := &servicelifecycle.Controller{DB: destination.db, Runtime: destination.runtime, Resolve: func(ctx context.Context, crew, ws, name string) (provider.CrewConfig, error) {
		var body, slug string
		if err := destination.db.QueryRowContext(ctx, `SELECT services_json,slug FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, crew, ws).Scan(&body, &slug); err != nil {
			return provider.CrewConfig{}, err
		}
		specs, err := declaredServiceSnapshots(body, crew, slug)
		if err != nil || len(specs) != 1 {
			return provider.CrewConfig{}, fmt.Errorf("invalid frozen fixture declaration: %w", err)
		}
		return provider.CrewConfig{ID: crew, Slug: slug, Services: []provider.CrewService{{ControllerManaged: true, QuotaEnforced: true, Name: name, Image: image, Command: []string{"sh", "-c", "sleep 600"}, Volumes: []provider.CrewServiceVolume{{Name: "data", Mount: "/data", QuotaBytes: quota.MinBytes, Generation: specs[0].Generation}}}}}, nil
	}}
	for _, crew := range crews {
		crew := crew
		t.Cleanup(func() {
			clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			_ = destination.runtime.StopCrewService(clean, crew, "unused", "database")
			_ = destination.runtime.DetachQuotaService(clean, crew, "database")
		})
	}
	controller.Reconcile(ctx)
	for _, crew := range crews {
		var observed string
		if err := destination.db.QueryRow(`SELECT observed_state FROM service_runtime_intents WHERE crew_id=? AND service_name='database'`, crew).Scan(&observed); err != nil || observed != "running" {
			t.Fatalf("restored controller did not restart service: %s %v", observed, err)
		}
	}
	// The Complete recovery path has its own physical proof; a workspace
	// roundtrip alone does not establish that the instance collector carries disks.
	instance, err := CreateInstanceBackup(ctx, source.db, InstanceOptions{OutputDir: t.TempDir(), Passphrase: passphrase, Actor: covAdminActor(), ServiceSnapshots: source.runtime, RecoveryKit: true})
	if err != nil {
		t.Fatal(err)
	}
	if instance.Manifest.Contents.ServiceSnapshots != 2 {
		t.Fatal("instance images missing")
	}
	recoveredDir := t.TempDir()
	report, err := RecoverInstance(ctx, RecoverOptions{BundlePath: instance.Path, Passphrase: passphrase, DataDir: recoveredDir, Drill: true})
	if err != nil {
		t.Fatal(err)
	}
	recoveredDB, err := database.Open("file:" + report.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredDB.Close()
	instanceHost := newSnapshotLiveHost(t, ctx, "instance-destination-fixture")
	instanceHost.runtime.SetServiceOperationGate(servicelifecycle.ServiceOperations(recoveredDB.DB))
	imported, err := LandRecoveredServices(ctx, recoveredDB.DB, recoveredDir, instanceHost.runtime)
	if err != nil || imported != 2 {
		t.Fatalf("instance physical landing: %d %v", imported, err)
	}
	for i, crew := range crews {
		var body string
		if err = recoveredDB.QueryRow(`SELECT services_json FROM crews WHERE id=?`, crew).Scan(&body); err != nil {
			t.Fatal(err)
		}
		specs, err := declaredServiceSnapshots(body, crew, "unused")
		if err != nil || len(specs) != 1 {
			t.Fatal(err)
		}
		key := specs[0].key()
		t.Cleanup(func() { _ = instanceHost.catalog.Remove(context.Background(), key) })
		d, err := instanceHost.catalog.Verify(ctx, key, quota.MinBytes)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(d.Mount, "owner-canary"))
		if err != nil || string(raw) != fmt.Sprintf("PRIVATE_OWNER_%d", i) {
			t.Fatalf("instance owner data: %q %v", raw, err)
		}
		info, err := os.Stat(filepath.Join(d.Mount, "owner-canary"))
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if key.Generation == 7 || info.Mode().Perm() != 0640 || stat.Uid != 1001 || stat.Gid != 1002 {
			t.Fatal("instance generation or ownership lost")
		}
	}
	t.Log("encrypted two-owner archive restored bytes, UID/GID/mode, original intents/version, fresh generations and controller-driven restart")
}
