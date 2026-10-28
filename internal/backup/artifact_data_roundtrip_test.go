package backup_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestArtifactDataBackupFreshAndForkPreserveDocumentKeys(t *testing.T) {
	source := openDurableArtifactBackupDB(t)
	ws := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('data-backup',?,'data-backup','Dataset','u_admin','{}')`, ws); err != nil {
		t.Fatal(err)
	}
	k := artifacts.VersionKey{WorkspaceID: ws, PageID: "data-backup"}
	auth := func(context.Context, *sql.Tx) (string, error) { return "u_admin", nil }
	v := artifacts.NewVersionStore(source, artifacts.VersionOptions{Enabled: true})
	if _, err := v.Apply(t.Context(), k, artifacts.VersionRequest{EntryPoint: "index.html", Files: map[string]artifacts.VersionFile{"index.html": {Content: []byte("<h1>Dataset</h1>"), MediaType: "text/html"}}}, auth); err != nil {
		t.Fatal(err)
	}
	d := artifacts.NewDataStore(source, artifacts.DataOptions{Enabled: true})
	for _, base := range []int64{0, 1} {
		if _, err := d.Batch(t.Context(), k, "sales", []artifacts.DataWrite{{ID: "stable-row", IfVersion: base, Value: json.RawMessage(`{"n":9007199254740993}`)}}, auth); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backup.MarshalDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "fork"}[fork], func(t *testing.T) {
			target := openDurableArtifactBackupDB(t)
			copy, err := backup.UnmarshalDump(raw)
			if err != nil {
				t.Fatal(err)
			}
			if fork {
				if err := backup.RemapIDs(t.Context(), target, copy); err != nil {
					t.Fatal(err)
				}
			}
			if err := backup.RestoreDump(t.Context(), target, copy); err != nil {
				t.Fatal(err)
			}
			var restored artifacts.VersionKey
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM artifact_data_collections`).Scan(&restored.WorkspaceID, &restored.PageID); err != nil {
				t.Fatal(err)
			}
			if fork && (restored.WorkspaceID == ws || restored.PageID == k.PageID) {
				t.Fatal("fork retained source scope")
			}
			docs, err := artifacts.NewDataStore(target, artifacts.DataOptions{Enabled: true}).Read(t.Context(), restored, "sales", auth)
			if err != nil || len(docs) != 1 || docs[0].ID != "stable-row" || docs[0].Version != 2 || docs[0].UpdatedBy != "u_admin" || string(docs[0].Value) != `{"n":9007199254740993}` {
				t.Fatalf("dataset changed: %+v %v", docs, err)
			}
		})
	}
}
