package backup_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

const integritySecret = "TOPSECRET-column-value-4711"

// A healthy database is recorded as ok, and the manifest carries the record.
func TestInstanceBackupRecordsHealthyDatabaseCheck(t *testing.T) {
	f := newInstanceFixture(t)
	created := f.create(t, true)
	dc := created.Manifest.Contents.Instance.DatabaseCheck
	if dc == nil || dc.Status != backup.DatabaseCheckOK || dc.FindingsTotal != 0 {
		t.Fatalf("database_check = %+v", dc)
	}
	if got := created.Manifest.Contents.Instance.DatabaseCheckState(); got != "ok" {
		t.Fatalf("state = %q", got)
	}
	for _, in := range created.Manifest.Contents.Incomplete {
		if in.Kind == backup.IncompleteDatabaseDamaged {
			t.Fatalf("healthy database marked damaged: %+v", in)
		}
	}
	c, err := backup.CheckBundleContents(context.Background(), created.Path, []age.Identity{f.identity}, "")
	if err != nil || !c.OK || c.ProofLevel != backup.ProofContents || c.DatabaseCheck == nil || !c.DatabaseCheck.Verified() {
		t.Fatalf("contents = %+v err=%v", c, err)
	}
}

// A damaged database keeps its backup, is recorded in the manifest and the
// gap list, and the manifest never holds a column value.
func TestInstanceBackupDamagedDatabaseIsRecordedWithoutValues(t *testing.T) {
	ctx := context.Background()
	f := newInstanceFixture(t)
	if _, err := f.db.ExecContext(ctx, "CREATE TABLE secret_fixture(value TEXT CHECK (length(value)>200))"); err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, "PRAGMA ignore_check_constraints=ON; INSERT INTO secret_fixture VALUES ('"+integritySecret+"'); PRAGMA ignore_check_constraints=OFF")
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	created := f.create(t, true)
	inst := created.Manifest.Contents.Instance
	if inst.DatabaseCheck == nil || inst.DatabaseCheck.Status != backup.DatabaseCheckDamaged || inst.DatabaseCheck.FindingsTotal == 0 {
		t.Fatalf("database_check = %+v", inst.DatabaseCheck)
	}
	var gap bool
	for _, in := range created.Manifest.Contents.Incomplete {
		gap = gap || in.Kind == backup.IncompleteDatabaseDamaged
	}
	if !gap {
		t.Fatalf("no database_damaged gap: %+v", created.Manifest.Contents.Incomplete)
	}
	// The manifest is stored in plaintext: read what is on disk.
	m, err := backup.Inspect(ctx, created.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	data, _ := os.ReadFile(created.Path)
	for name, b := range map[string][]byte{"inspected manifest": raw, "bundle file": data} {
		if strings.Contains(string(b), integritySecret) {
			t.Fatalf("%s contains a column value", name)
		}
	}
	if !strings.Contains(string(raw), "secret_fixture") {
		t.Fatalf("finding names no table: %s", raw)
	}
}

// A check that cannot finish is "error": the backup still succeeds and the
// database is not reported healthy anywhere.
func TestInstanceBackupDatabaseCheckErrorIsNotOK(t *testing.T) {
	f := newInstanceFixture(t)
	res, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "instance_admin"}, Recipients: []age.Recipient{f.identity.Recipient()}, Paths: f.paths,
		Quiesce: quiesce.New(), Poll: 5 * time.Millisecond, DatabaseCheckTimeout: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("a check that ran out must not fail the backup: %v", err)
	}
	dc := res.Manifest.Contents.Instance.DatabaseCheck
	if dc == nil || dc.Status != backup.DatabaseCheckError || dc.Verified() || dc.Error == "" {
		t.Fatalf("database_check = %+v", dc)
	}
	var gap bool
	for _, in := range res.Manifest.Contents.Incomplete {
		gap = gap || in.Kind == backup.IncompleteDatabaseUnchecked
	}
	if !gap {
		t.Fatalf("an unchecked database leaves no gap: %+v", res.Manifest.Contents.Incomplete)
	}
	if res.Manifest.Contents.Instance.DatabaseCheckState() == backup.DatabaseCheckOK {
		t.Fatal("error state reads as ok")
	}
}

// The check runs after the quiet window is released, never inside it.
func TestInstanceBackupDatabaseCheckRunsAfterWindowRelease(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	var holding, ran bool
	undo := backup.SetBeforeDatabaseCheckForTest(func() { ran, holding = true, ctrl.Holding() })
	defer undo()
	if _, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "instance_admin"}, Recipients: []age.Recipient{f.identity.Recipient()}, Paths: f.paths,
		Quiesce: ctrl, Poll: 5 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if !ran || holding {
		t.Fatalf("ran=%t windowStillOpen=%t", ran, holding)
	}
}

// A bundle written before the record existed reads as unverified, not ok,
// and a contents check still checks its database.
func TestInstanceBackupWithoutDatabaseCheckIsUnverified(t *testing.T) {
	var inst *backup.InstanceContents
	if got := inst.DatabaseCheckState(); got != backup.DatabaseCheckUnverified {
		t.Fatalf("nil instance state = %q", got)
	}
	if got := (&backup.InstanceContents{}).DatabaseCheckState(); got != backup.DatabaseCheckUnverified {
		t.Fatalf("no record state = %q", got)
	}
	var old backup.InstanceContents
	if err := json.Unmarshal([]byte(`{"workspaces":[],"stores":{},"database_bytes":1,"database_sha256":"x","recovery_kit":false,"hold_ms":0}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.DatabaseCheckState() != backup.DatabaseCheckUnverified {
		t.Fatalf("old manifest state = %q", old.DatabaseCheckState())
	}
}

// A damaged newest bundle must not cost the last intact checked one: it is
// never counted as a checked copy, so the older intact bundle stays.
func TestPlanRetentionDamagedNewestKeepsLastIntactCheckedCopy(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	cands := []backup.RetentionCandidate{
		{Path: "damaged-newest", Scope: backup.ScopeInstance, CreatedAt: now.Add(-1 * time.Hour), ProofLevel: backup.ProofChecksum, Incomplete: true},
		{Path: "intact-checked", Scope: backup.ScopeInstance, CreatedAt: now.Add(-48 * time.Hour), ProofLevel: backup.ProofContents, Complete: true},
		{Path: "older", Scope: backup.ScopeInstance, CreatedAt: now.Add(-72 * time.Hour), ProofLevel: backup.ProofChecksum, Complete: true},
	}
	_, drop := backup.PlanRetention(cands, backup.RetentionPolicy{KeepMin: 1}, now)
	for _, d := range drop {
		if d.Path == "damaged-newest" || d.Path == "intact-checked" {
			t.Fatalf("retention dropped %s", d.Path)
		}
	}
}

func plantOrphan(t *testing.T, f *instanceFixture, withCheckDamage bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmts := []string{
		"PRAGMA foreign_keys=OFF",
		"CREATE TABLE fk_parent(id TEXT PRIMARY KEY)",
		"CREATE TABLE fk_child(id TEXT PRIMARY KEY, parent_id TEXT REFERENCES fk_parent(id))",
		"INSERT INTO fk_child VALUES ('c1', 'missing-parent')",
	}
	if withCheckDamage {
		stmts = append(stmts,
			"CREATE TABLE chk_fixture(value TEXT CHECK (length(value)>200))",
			"PRAGMA ignore_check_constraints=ON",
			"INSERT INTO chk_fixture VALUES ('x')",
			"PRAGMA ignore_check_constraints=OFF")
	}
	for _, q := range stmts {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Orphaned foreign keys alone are a warning: status stays ok, nothing is
// listed as a gap, the contents check still passes and the drill is ok.
func TestInstanceBackupForeignKeyOrphansAreOnlyAWarning(t *testing.T) {
	ctx := context.Background()
	f := newInstanceFixture(t)
	plantOrphan(t, f, false)
	created := f.create(t, true)
	dc := created.Manifest.Contents.Instance.DatabaseCheck
	if dc == nil || dc.Status != backup.DatabaseCheckOK || dc.ForeignKeyTotal != 1 || dc.FindingsTotal != 0 {
		t.Fatalf("database_check = %+v", dc)
	}
	if len(created.Manifest.Contents.Incomplete) != 0 {
		t.Fatalf("orphans became a gap: %+v", created.Manifest.Contents.Incomplete)
	}
	if strings.Contains(strings.Join(dc.ForeignKeyFindings, ";"), "missing-parent") {
		t.Fatalf("finding carries a value: %v", dc.ForeignKeyFindings)
	}
	c, err := backup.CheckBundleContents(ctx, created.Path, []age.Identity{f.identity}, "")
	if err != nil || !c.OK || c.ProofLevel != backup.ProofContents || !strings.Contains(c.Detail, "warning") {
		t.Fatalf("contents = %+v err=%v", c, err)
	}
	d, err := backup.DrillInstance(ctx, backup.DrillOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, TempRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != backup.RestoreResultOK {
		t.Fatalf("drill = %s %v", d.Result, d.Failures())
	}
}

// A damaged integrity_check together with orphans is damaged.
func TestInstanceBackupCheckDamageWithOrphansIsDamaged(t *testing.T) {
	f := newInstanceFixture(t)
	plantOrphan(t, f, true)
	created := f.create(t, true)
	dc := created.Manifest.Contents.Instance.DatabaseCheck
	if dc == nil || dc.Status != backup.DatabaseCheckDamaged || dc.FindingsTotal == 0 || dc.ForeignKeyTotal != 1 {
		t.Fatalf("database_check = %+v", dc)
	}
}

// The newest copy whose database is not known damaged is never dropped
// because newer damaged copies took the floor.
func TestPlanRetentionKeepsNewestNonDamagedCopy(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	mk := func(name string, hours int, damaged bool, proof int) backup.RetentionCandidate {
		return backup.RetentionCandidate{Path: name, Scope: backup.ScopeInstance, CreatedAt: now.Add(-time.Duration(hours) * time.Hour),
			ProofLevel: proof, DatabaseDamaged: damaged, Incomplete: damaged}
	}
	dropped := func(c []backup.RetentionCandidate) map[string]bool {
		_, drop := backup.PlanRetention(c, backup.RetentionPolicy{KeepMin: 1}, now)
		out := map[string]bool{}
		for _, d := range drop {
			out[d.Path] = true
		}
		return out
	}
	t.Run("older proof 1 survives a damaged newest", func(t *testing.T) {
		d := dropped([]backup.RetentionCandidate{mk("bad", 1, true, 1), mk("good", 48, false, 1), mk("older", 72, false, 1)})
		if d["bad"] || d["good"] || !d["older"] {
			t.Fatalf("dropped = %v", d)
		}
	})
	t.Run("several damaged in a row", func(t *testing.T) {
		d := dropped([]backup.RetentionCandidate{mk("bad1", 1, true, 1), mk("bad2", 25, true, 1), mk("bad3", 49, true, 1), mk("good", 96, false, 1)})
		if d["good"] || d["bad1"] {
			t.Fatalf("dropped = %v", d)
		}
	})
	t.Run("an unverified older copy counts as not damaged", func(t *testing.T) {
		d := dropped([]backup.RetentionCandidate{mk("bad", 1, true, 1), mk("unverified", 48, false, 1)})
		if d["unverified"] {
			t.Fatalf("dropped = %v", d)
		}
	})
	t.Run("no intact copy keeps the floor only", func(t *testing.T) {
		d := dropped([]backup.RetentionCandidate{mk("bad1", 1, true, 1), mk("bad2", 25, true, 1)})
		if d["bad1"] || !d["bad2"] {
			t.Fatalf("dropped = %v", d)
		}
	})
}
