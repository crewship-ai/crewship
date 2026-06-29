package backup

// Coverage tests for runner_create.go — CreateBackup branch coverage
// against a fully-migrated SQLite DB (validation, container probe,
// guards, locking, scope routing, encryption modes) plus the pure
// helpers compatibleTargetsFor and buildContents.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/database"
)

// openMigratedDBCov returns a migrated on-disk SQLite DB (same recipe
// as the e2e harness, duplicated here because that helper lives in the
// external backup_test package).
func openMigratedDBCov(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/cov.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := database.SeedBundledSkills(context.Background(), db, logger); err != nil {
		t.Fatalf("seed skills: %v", err)
	}
	return db
}

// seedCovWorkspace inserts a minimal tenant and returns (workspaceID,
// crewID). idSuffix keeps concurrent tests from colliding on the
// process-wide backup guard.
func seedCovWorkspace(t *testing.T, db *sql.DB, idSuffix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	wsID := "ws_cov_" + idSuffix
	crewID := "c_cov_" + idSuffix
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		"u_cov_"+idSuffix, idSuffix+"@cov.test", "Cov"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		wsID, "Cov "+idSuffix, "cov-"+idSuffix); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		crewID, wsID, "Crew "+idSuffix, "crew-"+idSuffix); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status)
		 VALUES (?, ?, ?, ?, ?, 'IDLE')`,
		"a_cov_"+idSuffix, crewID, wsID, "Agent", "agent-"+idSuffix); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return wsID, crewID
}

func covAdminActor() Actor {
	return Actor{UserID: "u_admin", Email: "admin@cov.test", Role: "ADMIN"}
}

// probeErrOps fails ContainerExists; everything else delegates to an
// embedded fakeDockerOps so unrelated calls behave.
type probeErrOps struct {
	*fakeDockerOps
	err error
}

func (p *probeErrOps) ContainerExists(context.Context, string) (bool, error) {
	return false, p.err
}

func TestCreateBackup_ValidationShortCircuits(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		opts CreateOptions
		sub  string
	}{
		{"unsupported scope", CreateOptions{Scope: ScopeInstance, Actor: covAdminActor(), NoEncrypt: true}, "unsupported scope"},
		{"workspace scope without id", CreateOptions{Scope: ScopeWorkspace, Actor: covAdminActor(), NoEncrypt: true}, "WorkspaceID required"},
		{"crew scope without id", CreateOptions{Scope: ScopeCrew, Actor: covAdminActor(), NoEncrypt: true}, "CrewID required"},
		{"missing actor", CreateOptions{Scope: ScopeWorkspace, WorkspaceID: "w", NoEncrypt: true}, "Actor.UserID required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CreateBackup(ctx, nil, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %v, want %q", err, tc.sub)
			}
		})
	}
	t.Run("non-admin rejected", func(t *testing.T) {
		_, err := CreateBackup(ctx, nil, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: "w", NoEncrypt: true,
			Actor: Actor{UserID: "u1", Role: "MEMBER"},
		})
		if !errors.Is(err, ErrAdminRequired) {
			t.Fatalf("err = %v, want ErrAdminRequired", err)
		}
	})
}

func TestCreateBackup_WorkspaceNotFound(t *testing.T) {
	db := openMigratedDBCov(t)
	_, err := CreateBackup(context.Background(), db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: "ws_ghost",
		Actor: covAdminActor(), NoEncrypt: true,
	})
	if err == nil || !strings.Contains(err.Error(), "load workspace") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateBackup_CrewScope_HappyPath(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	_, crewID := seedCovWorkspace(t, db, "crewscope")

	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope:     ScopeCrew,
		CrewID:    crewID,
		OutputDir: t.TempDir(),
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if err != nil {
		t.Fatalf("CreateBackup crew scope: %v", err)
	}
	if !strings.Contains(res.Path, "crewship-crew-crew-crewscope-") {
		t.Errorf("crew bundle name must carry the CREW slug: %s", res.Path)
	}
	m := res.Manifest
	if m.Scope != ScopeCrew {
		t.Errorf("scope = %s", m.Scope)
	}
	if len(m.CompatibleTargets) != 1 || m.CompatibleTargets[0] != TargetSameInstance {
		t.Errorf("crew bundles must be same-instance only: %v", m.CompatibleTargets)
	}
	if m.Encryption.Enabled {
		t.Errorf("NoEncrypt bundle reports encryption enabled")
	}
	if len(m.Contents.Crews) != 1 || m.Contents.Crews[0].Slug != "crew-crewscope" {
		t.Errorf("contents crews = %+v", m.Contents.Crews)
	}
	// No container at backup time → all filesystem sections absent.
	if m.Contents.Crews[0].WorkspaceIncluded || m.Contents.Crews[0].MemoryIncluded {
		t.Errorf("sections must be excluded without a container: %+v", m.Contents.Crews[0])
	}
	// The bundle decodes and carries a crews row in its dump.
	verify, err := Verify(ctx, res.Path)
	if err != nil || !verify.Valid {
		t.Fatalf("Verify = (%+v, %v)", verify, err)
	}
}

func TestCreateBackup_RecipientsMode(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "recip")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	res, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir:  t.TempDir(),
		Actor:      covAdminActor(),
		Recipients: []age.Recipient{id.Recipient()},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	m := res.Manifest
	if !m.Encryption.Enabled || m.Encryption.Algorithm != EncryptionAlgorithm {
		t.Errorf("encryption = %+v", m.Encryption)
	}
	if len(m.Encryption.Recipients) != 1 || !strings.HasPrefix(m.Encryption.Recipients[0], "age1") {
		t.Errorf("recipients = %v", m.Encryption.Recipients)
	}
	// Restorable with the matching identity.
	restored, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
		Path:       res.Path,
		Identities: []age.Identity{id},
		Actor:      covAdminActor(),
	})
	if err != nil {
		t.Fatalf("RestoreBackup with identity: %v", err)
	}
	if restored.RowsInserted <= 0 {
		t.Errorf("rows inserted = %d", restored.RowsInserted)
	}
}

func TestCreateBackup_ContainerProbe(t *testing.T) {
	ctx := context.Background()

	t.Run("probe error fails the backup", func(t *testing.T) {
		db := openMigratedDBCov(t)
		wsID, _ := seedCovWorkspace(t, db, "probeerr")
		ops := &probeErrOps{fakeDockerOps: newFakeDockerOps(), err: errors.New("daemon unreachable")}
		_, err := CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID,
			OutputDir:         t.TempDir(),
			Actor:             covAdminActor(),
			NoEncrypt:         true,
			DockerOps:         ops,
			CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
		})
		if err == nil || !strings.Contains(err.Error(), "probe container") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("absent container clears the section flags", func(t *testing.T) {
		db := openMigratedDBCov(t)
		wsID, _ := seedCovWorkspace(t, db, "probeabsent")
		ops := newFakeDockerOps()
		ops.exists = false
		res, err := CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID,
			OutputDir:         t.TempDir(),
			Actor:             covAdminActor(),
			NoEncrypt:         true,
			DockerOps:         ops,
			CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
		})
		if err != nil {
			t.Fatalf("CreateBackup: %v", err)
		}
		c := res.Manifest.Contents.Crews[0]
		if c.WorkspaceIncluded || c.MemoryIncluded || len(c.VolumesIncluded) != 0 {
			t.Errorf("absent container must zero section flags: %+v", c)
		}
	})
	t.Run("present container collects and flags sections", func(t *testing.T) {
		db := openMigratedDBCov(t)
		wsID, _ := seedCovWorkspace(t, db, "probelive")
		ops := newFakeDockerOps()
		ops.workspace["main.py"] = []byte("print('hi')")
		res, err := CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID,
			OutputDir:         t.TempDir(),
			Actor:             covAdminActor(),
			NoEncrypt:         true,
			DockerOps:         ops,
			CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
		})
		if err != nil {
			t.Fatalf("CreateBackup: %v", err)
		}
		c := res.Manifest.Contents.Crews[0]
		if !c.WorkspaceIncluded || !c.MemoryIncluded {
			t.Errorf("live container must flag workspace+memory: %+v", c)
		}
		if strings.Join(c.VolumesIncluded, ",") != "home,tools" {
			t.Errorf("standard preset must include home+tools: %v", c.VolumesIncluded)
		}
	})
}

func TestCreateBackup_AgentRunningGuard(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, crewID := seedCovWorkspace(t, db, "agentbusy")
	if _, err := db.ExecContext(ctx,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status)
		 VALUES ('a_busy', ?, ?, 'Busy', 'busy', 'running')`, crewID, wsID); err != nil {
		t.Fatal(err)
	}
	_, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir: t.TempDir(),
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if !errors.Is(err, ErrAgentRunning) {
		t.Fatalf("err = %v, want ErrAgentRunning", err)
	}
}

func TestCreateBackup_LockHeld(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "lockheld")

	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(ctx, wsID, "someone-else", LockTimeout)
	if err != nil {
		t.Fatalf("pre-acquire: %v", err)
	}
	defer func() { _ = release(ctx) }()

	_, err = CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir: t.TempDir(),
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("err = %v, want ErrLockHeld", err)
	}
}

func TestCreateBackup_OutputDirError(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "outdir")
	_, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir: "bad\x00dir",
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if err == nil || !errors.Is(err, ErrUnsafeBackupPath) {
		t.Fatalf("err = %v, want unsafe-path failure from MkdirAll", err)
	}
}

// faultStorage wraps LocalStorageOps and fails specific calls so the
// runner's staging/seal/rename error branches can be reached.
type faultStorage struct {
	LocalStorageOps
	failCreateTempAt int // 1-based call index
	createTempCalls  int
	failOpenAt       int
	openCalls        int
	failCreate       bool
	failStat         bool
	failRename       bool
}

func (f *faultStorage) CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error) {
	f.createTempCalls++
	if f.failCreateTempAt > 0 && f.createTempCalls == f.failCreateTempAt {
		return nil, errors.New("faultStorage: createtemp refused")
	}
	return f.LocalStorageOps.CreateTemp(ctx, dir, pattern)
}

func (f *faultStorage) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	f.openCalls++
	if f.failOpenAt > 0 && f.openCalls == f.failOpenAt {
		return nil, errors.New("faultStorage: open refused")
	}
	return f.LocalStorageOps.Open(ctx, path)
}

func (f *faultStorage) Create(ctx context.Context, path string, perm os.FileMode) (io.WriteCloser, error) {
	if f.failCreate {
		return nil, errors.New("faultStorage: create refused")
	}
	return f.LocalStorageOps.Create(ctx, path, perm)
}

func (f *faultStorage) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	if f.failStat {
		return nil, errors.New("faultStorage: stat refused")
	}
	return f.LocalStorageOps.Stat(ctx, path)
}

func (f *faultStorage) Rename(ctx context.Context, oldPath, newPath string) error {
	if f.failRename {
		return errors.New("faultStorage: rename refused")
	}
	return f.LocalStorageOps.Rename(ctx, oldPath, newPath)
}

// TestCreateBackup_StorageFaultLadder walks the staging pipeline's
// failure points one by one. A single seeded source DB is reused — the
// workspace lock is released on every error path, which this test
// implicitly proves by succeeding on the final fault-free run.
func TestCreateBackup_StorageFaultLadder(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "faults")
	outDir := t.TempDir()

	run := func(st StorageOps) (*CreateResult, error) {
		return CreateBackup(ctx, db, CreateOptions{
			Scope: ScopeWorkspace, WorkspaceID: wsID,
			OutputDir: outDir,
			Actor:     covAdminActor(),
			NoEncrypt: true,
			Storage:   st,
		})
	}

	cases := []struct {
		name string
		st   *faultStorage
		sub  string
	}{
		{"payload temp", &faultStorage{failCreateTempAt: 1}, "create payload temp"},
		{"sealed temp", &faultStorage{failCreateTempAt: 2}, "create sealed temp"},
		{"reopen payload", &faultStorage{failOpenAt: 1}, "reopen payload"},
		{"partial create", &faultStorage{failCreate: true}, "open partial"},
		{"reopen sealed", &faultStorage{failOpenAt: 2}, "reopen sealed"},
		{"stat partial", &faultStorage{failStat: true}, "stat partial"},
		{"rename final", &faultStorage{failRename: true}, "rename final bundle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(tc.st)
			if err == nil || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %v, want %q", err, tc.sub)
			}
		})
	}

	// Every failure above must have released both guards and cleaned
	// its partials: a fault-free run on the same workspace succeeds and
	// the output dir holds exactly one bundle, zero .partial files.
	res, err := run(nil)
	if err != nil {
		t.Fatalf("fault-free run after failures: %v", err)
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	var bundles, partials int
	for _, e := range entries {
		switch {
		case strings.HasSuffix(e.Name(), ".partial"):
			partials++
		case strings.HasSuffix(e.Name(), ".tar.zst"):
			bundles++
		}
	}
	if bundles != 1 || partials != 0 {
		t.Errorf("outDir bundles=%d partials=%d (%v), want 1/0; res=%+v", bundles, partials, entries, res)
	}
}

func TestCreateBackup_CollectError(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "collecterr")
	ops := newFakeDockerOps()
	ops.copyFromErr = errors.New("stream torn mid-tar")
	_, err := CreateBackup(ctx, db, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir:         t.TempDir(),
		Actor:             covAdminActor(),
		NoEncrypt:         true,
		DockerOps:         ops,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err == nil || !strings.Contains(err.Error(), "stream torn mid-tar") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompatibleTargetsFor(t *testing.T) {
	if got := compatibleTargetsFor(ScopeCrew); len(got) != 1 || got[0] != TargetSameInstance {
		t.Errorf("crew = %v", got)
	}
	if got := compatibleTargetsFor(ScopeWorkspace); len(got) != 1 || got[0] != TargetAnyInstance {
		t.Errorf("workspace = %v", got)
	}
	if got := compatibleTargetsFor(ScopeInstance); len(got) != 1 || got[0] != TargetAnyInstance {
		t.Errorf("instance = %v", got)
	}
}

func TestBuildContents_PresetMatrix(t *testing.T) {
	target := &WorkspaceTarget{
		ID: "ws1", Slug: "ws-slug", Name: "WS",
		CrewTargets: []CrewTarget{
			{ID: "c1", Slug: "live", Name: "Live", ContainerID: "ctr1",
				DevcontainerConfig: "{}", MiseConfig: "[tools]", AgentCount: 2},
			{ID: "c2", Slug: "ghost", Name: "Ghost"}, // never provisioned
		},
	}
	cases := []struct {
		level       ScopeLevel
		wantVolumes bool
		wantSystem  bool
	}{
		{ScopeLevelQuick, false, false},
		{ScopeLevelStandard, true, false},
		{ScopeLevelFull, true, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.level), func(t *testing.T) {
			c := buildContents(target, tc.level)
			if c.Workspace == nil || c.Workspace.ID != "ws1" || c.Workspace.Slug != "ws-slug" {
				t.Fatalf("workspace summary = %+v", c.Workspace)
			}
			if len(c.Crews) != 2 {
				t.Fatalf("crews = %d", len(c.Crews))
			}
			live, ghost := c.Crews[0], c.Crews[1]
			if !live.WorkspaceIncluded || !live.MemoryIncluded {
				t.Errorf("live crew must include workspace+memory at every preset: %+v", live)
			}
			if !live.DevcontainerConfigIncluded || !live.MiseConfigIncluded {
				t.Errorf("config flags lost: %+v", live)
			}
			if live.AgentCount != 2 {
				t.Errorf("agent count = %d", live.AgentCount)
			}
			gotVolumes := len(live.VolumesIncluded) > 0
			if gotVolumes != tc.wantVolumes {
				t.Errorf("level %s volumes = %v, want %v", tc.level, live.VolumesIncluded, tc.wantVolumes)
			}
			if live.SystemIncluded != tc.wantSystem {
				t.Errorf("level %s system = %v, want %v", tc.level, live.SystemIncluded, tc.wantSystem)
			}
			// Ghost crew (no container) gets nothing regardless of preset.
			if ghost.WorkspaceIncluded || ghost.MemoryIncluded || len(ghost.VolumesIncluded) != 0 || ghost.SystemIncluded {
				t.Errorf("unprovisioned crew must carry no sections: %+v", ghost)
			}
		})
	}
}
