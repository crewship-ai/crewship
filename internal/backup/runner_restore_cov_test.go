package backup

// Coverage tests for runner_restore.go — RestoreBackup's validation /
// flag-conflict / schema-skew / decrypt / checksum / docker-phase /
// replace / no-op branches plus the small dump-rewrite helpers.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/database"
)

// writeRawBundle assembles a bundle whose payload is the given tar.zst
// bytes, sealed per opts, with the checksum stamped correctly unless
// overrideSHA is non-empty.
func writeRawBundle(t *testing.T, dir string, m *Manifest, payload []byte, opts WriteBundleOptions, overrideSHA string) string {
	t.Helper()
	var sealed bytes.Buffer
	sha, n, err := SealPayload(&sealed, bytes.NewReader(payload), opts)
	if err != nil {
		t.Fatal(err)
	}
	if overrideSHA != "" {
		sha = overrideSHA
	}
	m.Checksums.PayloadSHA256 = sha
	p := filepath.Join(dir, BundleFileName(m.Scope, "raw", m.CreatedAt))
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundleStream(f, m, &sealed, n); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func emptyPayloadTarZst(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func plainDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/plain.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRestoreBackup_InputValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("missing actor", func(t *testing.T) {
		_, err := RestoreBackup(ctx, nil, RestoreOptions{Path: "x"})
		if err == nil || !strings.Contains(err.Error(), "Actor.UserID required") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("non-admin", func(t *testing.T) {
		_, err := RestoreBackup(ctx, nil, RestoreOptions{Path: "x", Actor: Actor{UserID: "u", Role: "MEMBER"}})
		if !errors.Is(err, ErrAdminRequired) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing path", func(t *testing.T) {
		_, err := RestoreBackup(ctx, nil, RestoreOptions{Actor: covAdminActor()})
		if err == nil || !strings.Contains(err.Error(), "Path required") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing bundle file", func(t *testing.T) {
		_, err := RestoreBackup(ctx, nil, RestoreOptions{
			Path:  filepath.Join(t.TempDir(), "ghost.tar.zst"),
			Actor: covAdminActor(),
			// DryRun avoids the failure-webhook/metrics noise.
			DryRun: true,
		})
		if err == nil || !strings.Contains(err.Error(), "open bundle") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRestoreBackup_ScopeAndFlagConflicts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	payload := emptyPayloadTarZst(t)

	mkBundle := func(scope Scope) string {
		m := &Manifest{
			FormatVersion:     FormatVersion,
			Scope:             scope,
			CompatibleTargets: []Target{TargetAnyInstance},
			CreatedAt:         time.Now().UTC(),
			CreatedBy:         Actor{UserID: "u_cov"},
		}
		return writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")
	}
	wsBundle := mkBundle(ScopeWorkspace)
	crewBundle := mkBundle(ScopeCrew)
	instBundle := mkBundle(ScopeInstance)
	_ = dir

	cases := []struct {
		name string
		path string
		opts RestoreOptions
		sub  string
	}{
		{"instance scope rejected", instBundle, RestoreOptions{}, "instance scope restore is not supported"},
		{"both as-flags", wsBundle, RestoreOptions{AsWorkspace: "a", AsCrew: "b"}, "only one of --as-workspace or --as-crew"},
		{"as-workspace on crew bundle", crewBundle, RestoreOptions{AsWorkspace: "a"}, "--as-workspace is only valid for workspace-scope"},
		{"as-crew on workspace bundle", wsBundle, RestoreOptions{AsCrew: "b"}, "--as-crew is only valid for crew-scope"},
		{"replace plus as-workspace", wsBundle, RestoreOptions{Replace: true, AsWorkspace: "a"}, "--replace is incompatible"},
		{"replace on crew bundle", crewBundle, RestoreOptions{Replace: true}, "--replace is only supported for workspace-scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Path = tc.path
			opts.Actor = covAdminActor()
			opts.DryRun = true
			_, err := RestoreBackup(ctx, plainDB(t), opts)
			if !errors.Is(err, ErrInvalidScope) || !strings.Contains(err.Error(), tc.sub) {
				t.Fatalf("err = %v, want ErrInvalidScope with %q", err, tc.sub)
			}
		})
	}
}

func TestRestoreBackup_SchemaTooOld(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	m := &Manifest{
		FormatVersion:           FormatVersion,
		Scope:                   ScopeWorkspace,
		CompatibleTargets:       []Target{TargetAnyInstance},
		CreatedAt:               time.Now().UTC(),
		CreatedBy:               Actor{UserID: "u_cov"},
		SchemaMigrationVersions: []int{99999999},
	}
	p := writeRawBundle(t, t.TempDir(), m, emptyPayloadTarZst(t), WriteBundleOptions{NoEncrypt: true}, "")
	_, err := RestoreBackup(ctx, db, RestoreOptions{Path: p, Actor: covAdminActor(), DryRun: true})
	if !errors.Is(err, ErrSchemaTooOld) || !strings.Contains(err.Error(), "99999999") {
		t.Fatalf("err = %v, want ErrSchemaTooOld naming the missing version", err)
	}
}

func TestRestoreBackup_EncryptionHandling(t *testing.T) {
	ctx := context.Background()
	m := func() *Manifest {
		return &Manifest{
			FormatVersion:     FormatVersion,
			Scope:             ScopeWorkspace,
			CompatibleTargets: []Target{TargetAnyInstance},
			CreatedAt:         time.Now().UTC(),
			CreatedBy:         Actor{UserID: "u_cov"},
			Encryption:        Encryption{Enabled: true, Algorithm: EncryptionAlgorithm, KeyDerivation: "scrypt"},
		}
	}
	payload := emptyPayloadTarZst(t)

	t.Run("no key material supplied", func(t *testing.T) {
		p := writeRawBundle(t, t.TempDir(), m(), payload, WriteBundleOptions{Passphrase: "right"}, "")
		_, err := RestoreBackup(ctx, plainDB(t), RestoreOptions{Path: p, Actor: covAdminActor(), DryRun: true})
		if err == nil || !strings.Contains(err.Error(), "supply Passphrase or Identities") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong passphrase", func(t *testing.T) {
		p := writeRawBundle(t, t.TempDir(), m(), payload, WriteBundleOptions{Passphrase: "right"}, "")
		_, err := RestoreBackup(ctx, plainDB(t), RestoreOptions{
			Path: p, Passphrase: "wrong", Actor: covAdminActor(), DryRun: true,
		})
		if err == nil {
			t.Fatal("expected decryption failure")
		}
	})
}

func TestRestoreBackup_ChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	m := &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Now().UTC(),
		CreatedBy:         Actor{UserID: "u_cov"},
	}
	p := writeRawBundle(t, t.TempDir(), m, emptyPayloadTarZst(t), WriteBundleOptions{NoEncrypt: true},
		"sha256:"+strings.Repeat("a", 64))
	_, err := RestoreBackup(ctx, plainDB(t), RestoreOptions{Path: p, Actor: covAdminActor(), DryRun: true})
	if !errors.Is(err, ErrInvalidChecksum) {
		t.Fatalf("err = %v, want ErrInvalidChecksum", err)
	}
}

func TestRestoreBackup_ReplaceRequiresWorkspaceRow(t *testing.T) {
	ctx := context.Background()
	dumpJSON := []byte(`{"workspace_id":"ws1","tables":{"crews":[{"id":"c1","slug":"x"}]}}`)
	payload := buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}})
	m := &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Now().UTC(),
		CreatedBy:         Actor{UserID: "u_cov"},
	}
	p := writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")
	_, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
		Path: p, Actor: covAdminActor(), Replace: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires the bundle to carry a workspace row") {
		t.Fatalf("err = %v", err)
	}
}

// TestRestoreBackup_NoOpThenReplace drives the full DR loop: restore a
// real bundle once (rows land), again (every PK collides →
// ErrNoOpRestore with a populated result), then with --replace (target
// wiped, rows land again).
func TestRestoreBackup_NoOpThenReplace(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, source, "noopreplace")
	res, err := CreateBackup(ctx, source, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir: t.TempDir(),
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDBCov(t)
	logLines := []string{}
	logger := func(s string) { logLines = append(logLines, s) }

	first, err := RestoreBackup(ctx, target, RestoreOptions{
		Path: res.Path, Actor: covAdminActor(), Logger: logger,
	})
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	if first.RowsInserted <= 0 || first.RestoredWs != "cov-noopreplace" {
		t.Fatalf("first = %+v", first)
	}

	second, err := RestoreBackup(ctx, target, RestoreOptions{
		Path: res.Path, Actor: covAdminActor(),
	})
	if !errors.Is(err, ErrNoOpRestore) {
		t.Fatalf("second restore err = %v, want ErrNoOpRestore", err)
	}
	if second == nil || second.RowsInserted != 0 || second.RestoredWs != "cov-noopreplace" {
		t.Fatalf("no-op result must still carry metadata: %+v", second)
	}

	// Mutate the target so --replace has something to wipe.
	if _, err := target.ExecContext(ctx,
		`UPDATE workspaces SET name = 'tampered' WHERE id = ?`, wsID); err != nil {
		t.Fatal(err)
	}
	third, err := RestoreBackup(ctx, target, RestoreOptions{
		Path: res.Path, Actor: covAdminActor(), Replace: true, Logger: logger,
	})
	if err != nil {
		t.Fatalf("replace restore: %v", err)
	}
	if third.RowsInserted <= 0 {
		t.Fatalf("replace must re-insert rows: %+v", third)
	}
	var name string
	if err := target.QueryRowContext(ctx, `SELECT name FROM workspaces WHERE id = ?`, wsID).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Cov noopreplace" {
		t.Errorf("replace must reassert the bundle's row, got name=%q", name)
	}
	foundWipeLog := false
	for _, l := range logLines {
		if strings.Contains(l, "--replace: wiped target workspace state") {
			foundWipeLog = true
		}
	}
	if !foundWipeLog {
		t.Errorf("logger never reported the replace wipe: %v", logLines)
	}
}

func TestRestoreBackup_DockerPhase(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, source, "dockerphase")

	// Create with a live fake container so the manifest flags
	// WorkspaceIncluded=true and the bundle carries section data.
	createOps := newFakeDockerOps()
	createOps.workspace["report.md"] = []byte("# findings")
	res, err := CreateBackup(ctx, source, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir:         t.TempDir(),
		Actor:             covAdminActor(),
		NoEncrypt:         true,
		DockerOps:         createOps,
		CrewContainerName: func(_, slug string) string { return "ctr-" + slug },
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	t.Run("preflight error aborts before DB commit", func(t *testing.T) {
		target := openMigratedDBCov(t)
		ops := &probeErrOps{fakeDockerOps: newFakeDockerOps(), err: errors.New("daemon gone")}
		_, err := RestoreBackup(ctx, target, RestoreOptions{
			Path: res.Path, Actor: covAdminActor(),
			DockerOps:    ops,
			ContainerFor: func(_, slug string) string { return "ctr-" + slug },
		})
		if err == nil || !strings.Contains(err.Error(), "preflight crew") {
			t.Fatalf("err = %v", err)
		}
		// The PreCommit hook failed → tx rolled back → no rows.
		var n int
		if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE id = ?`, wsID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("docker preflight failure must roll the DB back, found %d rows", n)
		}
	})
	t.Run("unprovisioned container with bundle data refuses", func(t *testing.T) {
		ops := newFakeDockerOps()
		ops.exists = false
		_, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
			Path: res.Path, Actor: covAdminActor(),
			DockerOps:    ops,
			ContainerFor: func(_, slug string) string { return "ctr-" + slug },
		})
		if err == nil || !strings.Contains(err.Error(), "is not provisioned on this instance") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty container id is skipped", func(t *testing.T) {
		ops := newFakeDockerOps()
		got, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
			Path: res.Path, Actor: covAdminActor(),
			DockerOps:    ops,
			ContainerFor: func(_, slug string) string { return "" },
		})
		if err != nil {
			t.Fatalf("RestoreBackup: %v", err)
		}
		if got.RowsInserted <= 0 || got.DockerPhaseSkipped {
			t.Errorf("result = %+v", got)
		}
	})
	t.Run("live container receives the section data", func(t *testing.T) {
		ops := newFakeDockerOps()
		got, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
			Path: res.Path, Actor: covAdminActor(),
			DockerOps:    ops,
			ContainerFor: func(_, slug string) string { return "ctr-" + slug },
		})
		if err != nil {
			t.Fatalf("RestoreBackup: %v", err)
		}
		if got.RowsInserted <= 0 {
			t.Errorf("rows = %d", got.RowsInserted)
		}
		if string(ops.workspace["report.md"]) != "# findings" {
			t.Errorf("restored workspace = %v", ops.workspace)
		}
	})
}

func TestRestoreBackup_AsCrewRenamesAndSkipsDocker(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	_, crewID := seedCovWorkspace(t, source, "ascrew")
	res, err := CreateBackup(ctx, source, CreateOptions{
		Scope: ScopeCrew, CrewID: crewID,
		OutputDir: t.TempDir(),
		Actor:     covAdminActor(),
		NoEncrypt: true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	target := openMigratedDBCov(t)
	got, err := RestoreBackup(ctx, target, RestoreOptions{
		Path: res.Path, Actor: covAdminActor(), AsCrew: "forked-crew",
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if !got.DockerPhaseSkipped {
		t.Errorf("--as-crew must skip the docker phase")
	}
	var n int
	if err := target.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE slug = 'forked-crew'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("renamed crew rows = %d, want 1", n)
	}
}

func TestRestoreBackup_BackfillHookFailure(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, source, "backfillfail")

	applied := AppliedMigrationVersions(ctx, source)
	if len(applied) < 2 {
		t.Fatalf("need ≥2 migrations, got %d", len(applied))
	}
	skipped := applied[len(applied)-1]
	unregister := database.RegisterRestoreBackfill(skipped, func(context.Context, *sql.Tx, *slog.Logger) error {
		return errors.New("backfill exploded")
	})
	t.Cleanup(unregister)

	res, err := CreateBackup(ctx, source, CreateOptions{
		Scope: ScopeWorkspace, WorkspaceID: wsID,
		OutputDir:               t.TempDir(),
		Actor:                   covAdminActor(),
		NoEncrypt:               true,
		SchemaMigrationVersions: applied[:len(applied)-1],
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	logged := []string{}
	_, err = RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
		Path: res.Path, Actor: covAdminActor(),
		Logger: func(s string) { logged = append(logged, s) },
	})
	if !errors.Is(err, ErrRestoreBackfillFailed) {
		t.Fatalf("err = %v, want ErrRestoreBackfillFailed", err)
	}
	found := false
	for _, l := range logged {
		if strings.Contains(l, "restore backfill: replaying") {
			found = true
		}
	}
	if !found {
		t.Errorf("backfill replay never logged: %v", logged)
	}
}

func TestDumpHelpers_Rewrites(t *testing.T) {
	t.Run("firstWorkspaceID and slug", func(t *testing.T) {
		if firstWorkspaceID(nil) != "" || firstWorkspaceSlug(nil) != "" {
			t.Error("nil dump must yield empty values")
		}
		empty := &DBDump{Tables: map[string][]map[string]any{}}
		if firstWorkspaceID(empty) != "" || firstWorkspaceSlug(empty) != "" {
			t.Error("missing table must yield empty values")
		}
		nonString := &DBDump{Tables: map[string][]map[string]any{
			"workspaces": {{"id": 42, "slug": 43}},
		}}
		if firstWorkspaceID(nonString) != "" || firstWorkspaceSlug(nonString) != "" {
			t.Error("non-string values must yield empty, not panic")
		}
		good := &DBDump{Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws9", "slug": "nine"}},
		}}
		if firstWorkspaceID(good) != "ws9" || firstWorkspaceSlug(good) != "nine" {
			t.Errorf("got (%q, %q)", firstWorkspaceID(good), firstWorkspaceSlug(good))
		}
	})
	t.Run("rewriteWorkspaceSlug", func(t *testing.T) {
		// No workspaces table → no-op, no panic.
		rewriteWorkspaceSlug(&DBDump{Tables: map[string][]map[string]any{}}, "x")
		d := &DBDump{Tables: map[string][]map[string]any{
			"workspaces": {{"id": "w1", "slug": "old", "name": "Old"}},
		}}
		rewriteWorkspaceSlug(d, "new-slug")
		row := d.Tables["workspaces"][0]
		if row["slug"] != "new-slug" || row["name"] != "new-slug" || row["id"] != "w1" {
			t.Errorf("row = %v", row)
		}
	})
	t.Run("rewriteCrewSlug", func(t *testing.T) {
		rewriteCrewSlug(&DBDump{Tables: map[string][]map[string]any{}}, "c1", "x")
		d := &DBDump{Tables: map[string][]map[string]any{
			"crews": {
				{"id": "c0", "slug": "keep", "name": "Keep"},
				{"id": "c1", "slug": "old", "name": "Old"},
			},
		}}
		rewriteCrewSlug(d, "c1", "renamed")
		if d.Tables["crews"][0]["slug"] != "keep" {
			t.Errorf("untargeted crew touched: %v", d.Tables["crews"][0])
		}
		if d.Tables["crews"][1]["slug"] != "renamed" || d.Tables["crews"][1]["name"] != "renamed" {
			t.Errorf("targeted crew = %v", d.Tables["crews"][1])
		}
		// Unknown id → no-op.
		rewriteCrewSlug(d, "ghost", "z")
	})
}

func TestReplayRestoreBackfills_EmptyApplied(t *testing.T) {
	// DB without _migrations → applied empty → immediate nil.
	if err := replayRestoreBackfills(context.Background(), plainDB(t), []int{1, 2}, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}
