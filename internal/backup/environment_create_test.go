package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// A workspace backup with env_mode=complete carries each crew's
// environment, records its layer refs against the finished bundle, and
// rotating the older bundle away keeps every layer the newer one shares.
func TestCreateBackupCompleteEnvironmentsAndRotation(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "envcomplete")
	dir := t.TempDir()
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = ops.snap.Mounts[:3]
	ops.otherPaths = map[string]map[string][]byte{"/var/lib/postgresql": {"PG_VERSION": []byte("16")}}

	create := func() *CreateResult {
		t.Helper()
		res, err := CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: dir, Actor: covAdminActor(),
			NoEncrypt: true, DockerOps: ops, EnvMode: EnvModeComplete,
			CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
		})
		if err != nil {
			t.Fatalf("CreateBackup: %v", err)
		}
		if err := UpsertCatalogEntry(ctx, db, CatalogEntryFromResult(res, res.Manifest)); err != nil {
			t.Fatal(err)
		}
		return res
	}
	older := create()
	time.Sleep(1100 * time.Millisecond) // bundle names have second resolution
	newer := create()

	envs := older.Manifest.Contents.Environments
	if len(envs) != 1 || envs[0].Crew != "crew-envcomplete" || len(envs[0].Blobs) == 0 || envs[0].Platform != "linux/amd64" {
		t.Fatalf("manifest environments = %+v", envs)
	}
	for _, it := range older.Manifest.Contents.Incomplete {
		if it.Kind == IncompleteEnvironmentFailed {
			t.Fatalf("environment reported failed: %+v", it)
		}
	}
	if n := refCount(t, db, older.Path); n != len(envs[0].Blobs) {
		t.Errorf("older bundle refs = %d, want %d", n, len(envs[0].Blobs))
	}
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bundle_environment_refs WHERE bundle_ref LIKE 'pending:%'`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d pending refs left after finished backups", pending)
	}
	store := EnvironmentStoreFor(dir)
	newerBlobs := map[string]bool{}
	for _, d := range BundleEnvironmentBlobs(newer.Manifest) {
		newerBlobs[d] = true
	}
	var onlyOlder []string
	for _, d := range BundleEnvironmentBlobs(older.Manifest) {
		if !newerBlobs[d] {
			onlyOlder = append(onlyOlder, d)
		}
	}
	if len(onlyOlder) == 0 {
		t.Fatal("fixture: the older bundle should have a layer of its own")
	}

	dropped, err := RotateWithPolicy(ctx, db, dir, wsID, RetentionPolicy{KeepMin: 1}, false)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != older.Path {
		t.Fatalf("dropped = %v, want the older bundle", dropped)
	}
	if _, err := os.Stat(older.Path); !os.IsNotExist(err) {
		t.Errorf("older bundle still on disk")
	}
	for d := range newerBlobs {
		if !store.Has(d) {
			t.Errorf("layer %s the newer bundle needs was deleted", d)
		}
	}
	for _, d := range onlyOlder {
		if store.Has(d) {
			t.Errorf("layer %s only the rotated bundle needed survived", d)
		}
	}
	if refCount(t, db, older.Path) != 0 {
		t.Errorf("rotated bundle still holds refs")
	}
}

// A restore of a bundle with environments reports each one — here a dry
// run on the same server (layers in the store beside the bundle) says
// restored, and on a server of another architecture says rebuilt.
func TestRestoreBackupReportsEnvironments(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "envrestore")
	dir := t.TempDir()
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	ops.snap.Privileged = true
	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: dir, Actor: covAdminActor(),
		NoEncrypt: true, DockerOps: ops, EnvMode: EnvModeComplete,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err != nil {
		t.Fatal(err)
	}
	restore := func(ops *fakeEnvOps) *RestoreResult {
		t.Helper()
		out, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
			Path: res.Path, Actor: covAdminActor(), DryRun: true, DockerOps: ops,
			ContainerFor: func(_, slug string) string { return "ctr-" + slug },
		})
		if err != nil {
			t.Fatalf("RestoreBackup: %v", err)
		}
		return out
	}
	got := restore(ops)
	if len(got.Environments) != 1 || got.Environments[0].Result != EnvRestored || !strings.HasPrefix(got.Environments[0].Reason, "dry run") {
		t.Fatalf("environments = %+v", got.Environments)
	}
	if len(got.Environments[0].Unsafe) == 0 {
		t.Errorf("unsafe list not carried to the report")
	}
	arm := newFakeEnvOps()
	arm.runtime.Arch = "arm64"
	got = restore(arm)
	if len(got.Environments) != 1 || got.Environments[0].Result != EnvRebuilt {
		t.Fatalf("other architecture = %+v", got.Environments)
	}
	if ClassifyRestore(got, nil) != RestoreResultPartial {
		t.Errorf("a rebuilt environment must make the restore partial")
	}
}

// The ref table is not the only witness: a bundle on disk whose refs the
// database lost (a recovered database, a crash before the commit) still
// protects every layer its manifest names, and gets its refs back.
func TestEnvironmentGCHonoursBundlesTheDatabaseForgot(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "envforgot")
	dir := t.TempDir()
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: dir, Actor: covAdminActor(),
		NoEncrypt: true, DockerOps: ops, EnvMode: EnvModeComplete,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM bundle_environment_refs`); err != nil {
		t.Fatal(err)
	}
	store := EnvironmentStoreFor(dir)
	if _, err := CollectEnvironmentGarbage(ctx, db, store, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, d := range BundleEnvironmentBlobs(res.Manifest) {
		if !store.Has(d) {
			t.Errorf("layer %s of a bundle still on disk was collected", d)
		}
	}
	if n := refCount(t, db, res.Path); n != len(BundleEnvironmentBlobs(res.Manifest)) {
		t.Errorf("refs not reconciled: %d", n)
	}
}

// A crew whose environment cannot be captured keeps its files and the
// bundle says the environment is missing.
func TestCreateBackupEnvironmentFailureIsIncomplete(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "envfail")
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	ops.snap.Mounts = nil
	ops.commitErr = errors.New("commit refused")
	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: t.TempDir(), Actor: covAdminActor(),
		NoEncrypt: true, DockerOps: ops, EnvMode: EnvModeComplete,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if len(res.Manifest.Contents.Environments) != 0 {
		t.Errorf("environments = %+v", res.Manifest.Contents.Environments)
	}
	found := false
	for _, it := range res.Manifest.Contents.Incomplete {
		if it.Kind == IncompleteEnvironmentFailed && strings.Contains(it.Detail, "commit refused") {
			found = true
		}
	}
	if !found {
		t.Errorf("incomplete = %+v", res.Manifest.Contents.Incomplete)
	}
}

// Files-only backups (the default) touch no environment machinery.
func TestCreateBackupFilesOnlyTakesNoEnvironment(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "envfiles")
	ops := newFakeEnvOps()
	ops.snap = crewSnapshot()
	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID, OutputDir: t.TempDir(), Actor: covAdminActor(),
		NoEncrypt: true, DockerOps: ops,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.committed) != 0 || len(res.Manifest.Contents.Environments) != 0 {
		t.Errorf("files-only backup committed %v", ops.committed)
	}
}
