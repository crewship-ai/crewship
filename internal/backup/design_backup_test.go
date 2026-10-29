package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/design"
)

func TestDesignBackupPreservesUnsnapshottedStateAndHistoryOnFreshAndFork(t *testing.T) {
	source := openDurableArtifactBackupDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('design-backup-page',?,'design-backup','Design backup','u_admin','{}')`, workspace); err != nil {
		t.Fatal(err)
	}
	initial, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":40,"text":"Snapshot"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	live, err := design.Run(t.Context(), design.Request{State: initial.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"title","text":"Live change before snapshot"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	key := artifacts.VersionKey{WorkspaceID: workspace, PageID: "design-backup-page"}
	authorize := func(context.Context, *sql.Tx) (string, error) { return "u_admin", nil }
	store := artifacts.NewVersionStore(source, artifacts.VersionOptions{Enabled: true, ContentType: "design"})
	if _, err = store.Apply(t.Context(), key, artifacts.VersionRequest{EntryPoint: "design.yjs", Files: map[string]artifacts.VersionFile{"design.yjs": {Content: initial.State, MediaType: "application/vnd.yjs-update"}}}, authorize); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO artifact_design_documents(workspace_id,page_id,state,revision,saved_revision,updated_by,updated_at) VALUES(?,?,?,3,1,'u_admin','2026-10-28T20:00:00Z')`, workspace, key.PageID, live.State); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
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
			copied, err := backup.UnmarshalDump(raw)
			if err != nil {
				t.Fatal(err)
			}
			if fork {
				if err = backup.RemapIDs(t.Context(), target, copied); err != nil {
					t.Fatal(err)
				}
			}
			if err = backup.RestoreDump(t.Context(), target, copied); err != nil {
				t.Fatal(err)
			}
			var restored artifacts.VersionKey
			var state []byte
			var revision, saved int64
			var actor, updated string
			if err = target.QueryRow(`SELECT workspace_id,page_id,state,revision,saved_revision,updated_by,updated_at FROM artifact_design_documents`).Scan(&restored.WorkspaceID, &restored.PageID, &state, &revision, &saved, &actor, &updated); err != nil {
				t.Fatal(err)
			}
			if fork && (restored.WorkspaceID == workspace || restored.PageID == key.PageID) {
				t.Fatal("fork retained source Page scope")
			}
			if !bytes.Equal(state, live.State) || revision != 3 || saved != 1 || actor != "u_admin" || updated != "2026-10-28T20:00:00Z" {
				t.Fatal("backup changed live unsnapshotted bytes or checkpoint metadata")
			}
			reopened, err := design.Run(t.Context(), design.Request{State: state})
			if err != nil || len(reopened.Nodes) != 1 || !bytes.Contains(reopened.Nodes[0], []byte("Live change before snapshot")) {
				t.Fatalf("restored live canvas unavailable: %s %v", reopened.Nodes, err)
			}
			version, err := artifacts.NewVersionStore(target, artifacts.VersionOptions{Enabled: true, ContentType: "design"}).ReadVersion(t.Context(), restored, 1, authorize)
			if err != nil || !bytes.Equal(version.Files["design.yjs"].Content, initial.State) {
				t.Fatalf("immutable retained version changed: %v", err)
			}
		})
	}
}
