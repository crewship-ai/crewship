package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestRunAdmissionTargetLossAndSchemaDowngradeBeforeCallback(t *testing.T) {
	for _, kind := range []string{"marked-omitted", "marked-collision", "orphan-payload", "target-old", "source-modern-omitted", "temp-shadow", "unknown-view", "foreign-run-owner"} {
		t.Run(kind, func(t *testing.T) {
			db := admissionGuardDB(t, kind != "target-old")
			d := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"workspaces": {{"id": "admission-w", "slug": "admission"}}}}
			switch kind {
			case "foreign-run-owner":
				admissionGuardExec(t, db, `UPDATE pipeline_runs SET workspace_id='foreign',admission_required_format=1 WHERE id='admission-r'`)
			case "marked-omitted", "marked-collision", "temp-shadow":
				admissionGuardExec(t, db, `UPDATE pipeline_runs SET admission_required_format=1 WHERE id='admission-r'`)
			case "orphan-payload":
				admissionGuardExec(t, db, `INSERT INTO pipeline_run_admissions VALUES('a','admission-w','missing',1,X'01','fixture')`)
			case "target-old":
				d.Tables["pipeline_runs"] = []map[string]any{{"id": "admission-r", "workspace_id": "admission-w", "admission_required_format": 0, "admission_id": nil}}
			case "source-modern-omitted":
				admissionGuardExec(t, db, `CREATE TABLE _migrations(version INTEGER,name TEXT)`)
				admissionGuardExec(t, db, `INSERT INTO _migrations VALUES(17,'pipeline_run_admissions')`)
				d.Tables["pipeline_runs"] = []map[string]any{{"id": "admission-r"}}
			case "unknown-view":
				admissionGuardExec(t, db, `CREATE VIEW pipeline_run_admissions_future AS SELECT 1`)
			}
			if kind == "marked-collision" {
				d.WorkspaceID = "different"
				delete(d.Tables, "workspaces")
				d.Tables["pipeline_runs"] = []map[string]any{{"id": "admission-r", "workspace_id": "different"}}
			}
			if kind == "temp-shadow" {
				admissionGuardExec(t, db, `CREATE TEMP TABLE pipeline_runs(id TEXT,workspace_id TEXT,admission_required_format INTEGER,admission_id TEXT)`)
			}
			before := admissionGuardSnapshot(t, db)
			called := false
			_, err := RestoreDumpTxHooks(t.Context(), db, d, &RestoreDumpHooks{legacySourceVersions: []int{17}, PreInsert: func(context.Context, *sql.Tx) error { called = true; return nil }})
			if !errors.Is(err, ErrRunAdmissionTransportClosed) || called || admissionGuardSnapshot(t, db) != before {
				t.Fatalf("target downgrade was not fenced: %v called=%v", err, called)
			}
		})
	}
}
func TestRunAdmissionClosedBundleBeforeForkReplace(t *testing.T) {
	for _, mode := range []string{"plain", "fork", "replace"} {
		for _, input := range []string{"payload", "modern-missing"} {
			t.Run(mode+"/"+input, func(t *testing.T) {
				db, frontier := admissionNativeBundleDB(t)
				d := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"workspaces": {{"id": "admission-w", "slug": "admission"}}, "pipeline_runs": {{"id": "admission-r"}}}}
				if input == "payload" {
					d.Tables["pipeline_run_admissions"] = []map[string]any{{"id": "synthetic", "payload": "untrusted"}}
				}
				raw, e := json.Marshal(d)
				if e != nil {
					t.Fatal(e)
				}
				m := covManifestForWorkspace("admission-w", time.Now().UTC())
				m.SchemaMigrationVersions = []int{frontier}
				m.Contents.TableRowCounts = tableRowCounts(d)
				path := writeRawBundle(t, t.TempDir(), m, buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: raw}}), WriteBundleOptions{NoEncrypt: true}, "")
				original, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				before := admissionGuardSnapshot(t, db)
				opts := RestoreOptions{Path: path, Actor: covAdminActor(), DryRun: true}
				if mode == "fork" {
					opts.AsWorkspace = "synthetic-fork"
				}
				if mode == "replace" {
					opts.Replace = true
				}
				_, e = RestoreBackup(t.Context(), db, opts)
				after, re := os.ReadFile(path)
				if !errors.Is(e, ErrRunAdmissionTransportClosed) || re != nil || !bytes.Equal(original, after) || before != admissionGuardSnapshot(t, db) {
					t.Fatalf("closed bundle modified source/target: %v read=%v", e, re)
				}
			})
		}
	}
}
func TestRunAdmissionReplaceChecksResolvedSlugBeforeDelete(t *testing.T) {
	db := admissionGuardDB(t, true)
	admissionGuardExec(t, db, `UPDATE pipeline_runs SET admission_required_format=1 WHERE id='admission-r'`)
	before := admissionGuardSnapshot(t, db)
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	_, e = ReplaceWorkspaceContents(t.Context(), tx, "different-id", "admission")
	if re := tx.Rollback(); re != nil {
		t.Fatal(re)
	}
	if !errors.Is(e, ErrRunAdmissionTransportClosed) || before != admissionGuardSnapshot(t, db) {
		t.Fatalf("resolved target lost: %v", e)
	}
}

func TestRunAdmissionMainSlugTargetSurvivesTempWorkspaceShadow(t *testing.T) {
	for _, entry := range []string{"core", "replace"} {
		t.Run(entry, func(t *testing.T) {
			db := admissionGuardDB(t, true)
			admissionGuardExec(t, db, `UPDATE pipeline_runs SET admission_required_format=1 WHERE id='admission-r'`)
			admissionGuardExec(t, db, `CREATE TEMP TABLE workspaces(id TEXT,slug TEXT)`)
			before := admissionGuardSnapshot(t, db)
			called := false
			var err error
			if entry == "core" {
				_, err = RestoreDumpTxHooks(t.Context(), db, &DBDump{WorkspaceID: "different", Tables: map[string][]map[string]any{"workspaces": {{"id": "different", "slug": "admission"}}}}, &RestoreDumpHooks{PreInsert: func(context.Context, *sql.Tx) error { called = true; return nil }})
			} else {
				tx, e := db.BeginTx(t.Context(), nil)
				if e != nil {
					t.Fatal(e)
				}
				_, err = ReplaceWorkspaceContents(t.Context(), tx, "different", "admission")
				if e = tx.Rollback(); e != nil {
					t.Fatal(e)
				}
			}
			if !errors.Is(err, ErrRunAdmissionTransportClosed) || called || before != admissionGuardSnapshot(t, db) {
				t.Fatalf("TEMP hid protected main slug target: %v called=%v", err, called)
			}
		})
	}
}

// Native positive transport fixture; this creates no runtime admission proof.
func admissionNativeBundleDB(t *testing.T) (*sql.DB, int) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	admissionGuardExec(t, db, `INSERT INTO users(id,email,full_name) VALUES('u_admin','admin@cov.test','Admin')`)
	admissionGuardExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('admission-w','Admission','admission')`)
	admissionGuardExec(t, db, `INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash) VALUES('admission-p','admission-w','Admission','admission','{}',?)`, pipeline.DefinitionHash([]byte(`{}`)))
	admissionGuardExec(t, db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES('admission-r','admission-w','admission-p','admission','completed','fixture')`)
	var present int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pipeline_runs') WHERE name='admission_required_format'`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present == 0 {
		for _, q := range []string{`ALTER TABLE pipeline_runs ADD COLUMN admission_required_format INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE pipeline_runs ADD COLUMN admission_id TEXT`, `CREATE TABLE pipeline_run_admissions(id TEXT PRIMARY KEY,workspace_id TEXT,run_id TEXT,format INTEGER,payload BLOB,created_at TEXT)`} {
			admissionGuardExec(t, db, q)
		}
	}
	var frontier int
	err := db.QueryRow(`SELECT version FROM main._migrations WHERE name='pipeline_run_admissions'`).Scan(&frontier)
	if errors.Is(err, sql.ErrNoRows) {
		// A1 is before DDL: synthetic TEST-ONLY frontier strengthens a negative input
		// claim. Existing migration versions/names stay untouched; it is not authority.
		if err = db.QueryRow(`SELECT MAX(version)+1 FROM main._migrations`).Scan(&frontier); err != nil {
			t.Fatal(err)
		}
		admissionGuardExec(t, db, `INSERT INTO main._migrations(version,name) VALUES(?,'pipeline_run_admissions')`, frontier)
	} else if err != nil {
		t.Fatal(err)
	}
	return db, frontier
}
func TestRunAdmissionNativeEmptyBundleCompatibility(t *testing.T) {
	for _, mode := range []string{"plain", "fork", "replace"} {
		t.Run(mode, func(t *testing.T) {
			db, frontier := admissionNativeBundleDB(t)
			d := &DBDump{WorkspaceID: "admission-w", Tables: map[string][]map[string]any{"workspaces": {{"id": "admission-w", "slug": "admission", "name": "Admission"}}, "pipeline_run_admissions": {}}}
			raw, e := json.Marshal(d)
			if e != nil {
				t.Fatal(e)
			}
			m := covManifestForWorkspace("admission-w", time.Now().UTC())
			m.SchemaMigrationVersions = []int{frontier}
			m.Contents.TableRowCounts = tableRowCounts(d)
			path := writeRawBundle(t, t.TempDir(), m, buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: raw}}), WriteBundleOptions{NoEncrypt: true}, "")
			original, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			before := admissionGuardSnapshot(t, db)
			opts := RestoreOptions{Path: path, Actor: covAdminActor(), DryRun: true}
			if mode == "fork" {
				opts.AsWorkspace = "synthetic-fork"
			}
			if mode == "replace" {
				opts.Replace = true
			}
			result, e := RestoreBackup(t.Context(), db, opts)
			after, re := os.ReadFile(path)
			if e != nil || result == nil || re != nil || !bytes.Equal(original, after) || before != admissionGuardSnapshot(t, db) {
				t.Fatalf("native empty bundle compatibility: %v read=%v", e, re)
			}
		})
	}
}
