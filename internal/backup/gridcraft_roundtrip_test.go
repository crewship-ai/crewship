package backup_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

type backupWorkbookEngine struct{}

func (backupWorkbookEngine) Identity() string { return "backup-fixture@1" }
func (backupWorkbookEngine) Prepare(_ context.Context, b appengine.Binding, _ appengine.State, c appengine.Command) (appengine.Candidate, error) {
	return appengine.NewCandidate(b, c.Snapshot, []string{"*"}), nil
}
func TestGridCraftBackupWorkspaceRoundtripFencesRuntimeAndForkReplay(t *testing.T) {
	source := workbookBackupDB(t)
	ws := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('workbook-backup-page',?,'workbook-backup','Workbook','u_admin',1,'{}')`, ws); err != nil {
		t.Fatal(err)
	}
	auth := func(context.Context, *sql.Tx) (appengine.Principal, error) {
		return appengine.Principal{AuthorityID: "user:u_admin", MandateID: "human-import", Targets: []string{"*"}}, nil
	}
	key := appengine.Key{WorkspaceID: ws, PageID: "workbook-backup-page"}
	request := appengine.Request{MutationID: "original-import", MandateID: "human-import", Command: appengine.Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{"name":"Sheet1","cells":{"bands":{},"len":0}}]}`)}}
	receipt, err := appengine.New(source, appengine.Options{Enabled: true}).Execute(t.Context(), key, request, auth, backupWorkbookEngine{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until) VALUES(?,'workbook-backup-page','user:u_admin','pending','pending-hash','in_progress',7,4102444800000)`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) VALUES(?,'workbook-backup-page','live-intent','u_admin','source-run',1,1,1,1,'["Sheet1!B2"]','intent-hash')`, ws); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "workbook roundtrip test passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := workbookBackupDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "workbook roundtrip test passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "workbook-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var restored appengine.Key
			var generation int64
			if err := target.QueryRow(`SELECT workspace_id,page_id,generation FROM gridcraft_heads`).Scan(&restored.WorkspaceID, &restored.PageID, &generation); err != nil || generation != 2 {
				t.Fatalf("generation=%d %v", generation, err)
			}
			var intents int
			if err := target.QueryRow(`SELECT count(*) FROM gridcraft_intents`).Scan(&intents); err != nil || intents != 0 {
				t.Fatalf("live intent restored: %d %v", intents, err)
			}
			var state string
			var token, until int64
			if err := target.QueryRow(`SELECT state,token,lease_until FROM gridcraft_operations WHERE mutation_id='pending'`).Scan(&state, &token, &until); err != nil || state != "failed" || token != 8 || until != 0 {
				t.Fatalf("pending writer revived: %s %d %d %v", state, token, until, err)
			}
			store := appengine.New(target, appengine.Options{Enabled: true})
			read, err := store.Read(t.Context(), restored, auth)
			if err != nil || read.Rev != 1 || string(read.Snapshot) != string(request.Command.Snapshot) {
				t.Fatalf("content changed: %+v %v", read, err)
			}
			replay, err := store.Execute(t.Context(), restored, request, auth, backupWorkbookEngine{})
			if fork {
				if restored.PageID == key.PageID || !errors.Is(err, appengine.ErrConflict) {
					t.Fatalf("fork replay: %s %+v %v", restored.PageID, replay, err)
				}
				if _, err := store.Status(t.Context(), restored, request.MutationID, auth); !errors.Is(err, appengine.ErrConflict) {
					t.Fatalf("source receipt presented as fork operation: %v", err)
				}
			} else if err != nil || replay.Rev != receipt.Rev || replay.At != receipt.At {
				t.Fatalf("receipt changed: %+v %v", replay, err)
			}
		})
	}
}

func workbookBackupDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedDB(t)
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	full, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { full.Close() })
	return full.DB
}

func TestGridCraftPhysicalRecoveryDropsIntentsAndFencesCandidates(t *testing.T) {
	f := newInstanceFixture(t)
	for _, query := range []string{
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('physical-workbook','ws_e2e_1','physical-workbook','Workbook','u_admin',1,'{}')`,
		`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('physical-workbook','ws_e2e_1',1,1,7,'{"sheets":[]}')`,
		`INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until) VALUES('ws_e2e_1','physical-workbook','user:u_admin','pending','hash','in_progress',41,4102444800000)`,
		`INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) VALUES('ws_e2e_1','physical-workbook','source-intent','u_admin','source-run',1,7,1,1,'["Sheet1!B2"]','hash')`,
	} {
		if _, err := f.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered-workbook")
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var generation, token, until int64
	var state string
	var intents int
	if err = db.QueryRow(`SELECT generation FROM gridcraft_heads`).Scan(&generation); err != nil || generation != 8 {
		t.Fatalf("generation=%d %v", generation, err)
	}
	if err = db.QueryRow(`SELECT state,token,lease_until FROM gridcraft_operations`).Scan(&state, &token, &until); err != nil || state != "failed" || token != 42 || until != 0 {
		t.Fatalf("old candidate survived: %s %d %d %v", state, token, until, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM gridcraft_intents`).Scan(&intents); err != nil || intents != 0 {
		t.Fatalf("old intent survived: %d %v", intents, err)
	}
	if !strings.Contains(strings.Join(report.RuntimeReset, "\n"), "workbook") {
		t.Fatal("workbook fencing was not reported")
	}
}
