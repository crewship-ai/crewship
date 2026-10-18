package backup_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Existing real DR provenance and recordingOps exercise addressing only, not a live runtime.
func TestRunAdmissionFilesOnlyChecksActualDestinationBeforeCallbacks(t *testing.T) {
	for _, kind := range []string{"unknown-zero", "required-null", "payload"} {
		t.Run(kind, func(t *testing.T) {
			db := openMigratedDB(t)
			ctx := t.Context()
			source := seedWorkspace(t, db)
			actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
			containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }
			const passphrase = "synthetic-admission-resume"
			created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: source, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase, CrewContainerName: containerFor, DockerOps: &recordingOps{}})
			if err != nil {
				t.Fatalf("SETUP backup: %v", err)
			}
			fork, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, AsWorkspace: "admission-fork", Actor: actor, DockerOps: &recordingOps{}, ContainerFor: containerFor})
			if err != nil {
				t.Fatalf("SETUP real fork/provenance: %v", err)
			}
			if fork.RestoredWorkspaceID == "" || fork.RestoredWorkspaceID == source {
				t.Fatal("SETUP destination not distinct")
			}
			// A1 precedes DDL. On that baseline only, materialize the proposed shape as a test fixture;
			// after A2, use the actual migration/constraints unchanged.
			var present int
			if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pipeline_runs') WHERE name='admission_required_format'`).Scan(&present); err != nil {
				t.Fatal(err)
			}
			if present == 0 {
				for _, q := range []string{`ALTER TABLE pipeline_runs ADD COLUMN admission_required_format INTEGER NOT NULL DEFAULT 0`, `ALTER TABLE pipeline_runs ADD COLUMN admission_id TEXT`, `CREATE TABLE pipeline_run_admissions(id TEXT PRIMARY KEY,workspace_id TEXT,run_id TEXT,format INTEGER,payload BLOB,created_at TEXT)`} {
					if _, err = db.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err = db.Exec(`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash) VALUES('resume-p',?,'Resume','resume','{}',?)`, fork.RestoredWorkspaceID, pipeline.DefinitionHash([]byte(`{}`))); err != nil {
				t.Fatal(err)
			}
			required := 0
			if kind == "required-null" {
				required = 1
			}
			if _, err = db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,admission_required_format) VALUES('resume-r',?,'resume-p','resume','running','fixture',?)`, fork.RestoredWorkspaceID, required); err != nil {
				t.Fatal(err)
			}
			if kind == "payload" {
				if _, err = db.Exec(`INSERT INTO pipeline_run_admissions(id,workspace_id,run_id,format,payload,created_at) VALUES('resume-a',?,'resume-r',1,X'01','fixture')`, fork.RestoredWorkspaceID); err != nil {
					t.Fatal(err)
				}
			}
			original, err := os.ReadFile(created.Path)
			if err != nil {
				t.Fatal(err)
			}
			ops := &recordingOps{}
			callbacks := 0
			result, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor, FilesOnly: true, ResumeWorkspaceID: fork.RestoredWorkspaceID, DockerOps: ops, ContainerFor: func(id, slug string) string { callbacks++; return containerFor(id, slug) }})
			if kind == "unknown-zero" {
				if err != nil || result == nil || result.CrewsRestored == 0 || callbacks == 0 || len(ops.written) == 0 {
					t.Fatalf("UNKNOWN0 DR compatibility failed: %v callbacks=%d", err, callbacks)
				}
			} else if !errors.Is(err, backup.ErrRunAdmissionTransportClosed) || callbacks != 0 || len(ops.written) != 0 {
				t.Fatalf("protected actual destination reached callbacks: %v callbacks=%d", err, callbacks)
			}
			after, re := os.ReadFile(created.Path)
			if re != nil || !bytes.Equal(original, after) {
				t.Fatal("bundle changed")
			}
			var got int
			var ref any
			if err = db.QueryRow(`SELECT admission_required_format,admission_id FROM pipeline_runs WHERE id='resume-r'`).Scan(&got, &ref); err != nil || got != required || ref != nil {
				t.Fatalf("destination marker changed: %d %v %v", got, ref, err)
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM pipeline_run_admissions WHERE id='resume-a'`).Scan(&count); err != nil || count != map[bool]int{false: 0, true: 1}[kind == "payload"] {
				t.Fatalf("destination receipt changed: %d %v", count, err)
			}
		})
	}
}
