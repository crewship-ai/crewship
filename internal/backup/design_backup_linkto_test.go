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

func designBackupLinks(t *testing.T, label string, state []byte) map[string]any {
	t.Helper()
	result, err := design.Run(t.Context(), design.Request{State: state})
	if err != nil {
		t.Fatalf("%s: reopen: %v", label, err)
	}
	links := map[string]any{}
	for _, raw := range result.Nodes {
		var node map[string]any
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("%s: decode %s: %v", label, raw, err)
		}
		if link, ok := node["linkTo"]; ok {
			links[node["id"].(string)] = link
		}
	}
	return links
}

func assertBackupLinks(t *testing.T, label string, got map[string]any, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: links %v, want %v", label, got, want)
	}
	for id, link := range want {
		if got[id] != link {
			t.Errorf("%s: %s linkTo %v, want %q", label, id, got[id], link)
		}
	}
}

// Prototype links (#4449) live inside the opaque Yjs bytes. A backup restore
// and a workspace fork must keep them in the live document and in retained
// history, including a link to a screen that no longer exists.
func TestDesignBackupPreservesPrototypeLinksOnFreshAndFork(t *testing.T) {
	source := openDurableArtifactBackupDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('design-link-page',?,'design-links','Design links','u_admin','{}')`, workspace); err != nil {
		t.Fatal(err)
	}
	snapshot, err := design.Run(t.Context(), design.Request{Operations: json.RawMessage(`[
		{"op":"upsert","node":{"id":"home","type":"frame","x":0,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"detail","type":"frame","x":400,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"gone","type":"frame","x":800,"y":0,"width":360,"height":640}},
		{"op":"upsert","node":{"id":"to-detail","type":"rect","parentId":"home","x":24,"y":24,"width":120,"height":48,"linkTo":"detail"}},
		{"op":"upsert","node":{"id":"to-gone","type":"rect","parentId":"home","x":24,"y":96,"width":120,"height":48,"linkTo":"gone"}},
		{"op":"delete","id":"gone"}
	]`)})
	if err != nil {
		t.Fatal(err)
	}
	// Unsnapshotted live edit: retarget one link and clear nothing else.
	live, err := design.Run(t.Context(), design.Request{State: snapshot.State, Operations: json.RawMessage(`[{"op":"upsert","node":{"id":"to-detail","linkTo":"home"}}]`)})
	if err != nil {
		t.Fatal(err)
	}
	wantSnapshot := map[string]string{"to-detail": "detail", "to-gone": "gone"}
	wantLive := map[string]string{"to-detail": "home", "to-gone": "gone"}
	assertBackupLinks(t, "source snapshot", designBackupLinks(t, "source snapshot", snapshot.State), wantSnapshot)
	assertBackupLinks(t, "source live", designBackupLinks(t, "source live", live.State), wantLive)

	key := artifacts.VersionKey{WorkspaceID: workspace, PageID: "design-link-page"}
	authorize := func(context.Context, *sql.Tx) (string, error) { return "u_admin", nil }
	store := artifacts.NewVersionStore(source, artifacts.VersionOptions{Enabled: true, ContentType: "design"})
	if _, err = store.Apply(t.Context(), key, artifacts.VersionRequest{EntryPoint: "design.yjs", Files: map[string]artifacts.VersionFile{"design.yjs": {Content: snapshot.State, MediaType: "application/vnd.yjs-update"}}}, authorize); err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO artifact_design_documents(workspace_id,page_id,state,revision,saved_revision,updated_by,updated_at) VALUES(?,?,?,2,1,'u_admin','2026-11-01T08:00:00Z')`, workspace, key.PageID, live.State); err != nil {
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
			if err = target.QueryRow(`SELECT workspace_id,page_id,state FROM artifact_design_documents`).Scan(&restored.WorkspaceID, &restored.PageID, &state); err != nil {
				t.Fatal(err)
			}
			if fork && (restored.WorkspaceID == workspace || restored.PageID == key.PageID) {
				t.Fatal("fork retained source Page scope")
			}
			if !bytes.Equal(state, live.State) {
				t.Fatal("backup changed live Yjs bytes carrying prototype links")
			}
			assertBackupLinks(t, "restored live", designBackupLinks(t, "restored live", state), wantLive)
			version, err := artifacts.NewVersionStore(target, artifacts.VersionOptions{Enabled: true, ContentType: "design"}).ReadVersion(t.Context(), restored, 1, authorize)
			if err != nil {
				t.Fatalf("read retained version: %v", err)
			}
			retained := version.Files["design.yjs"].Content
			if !bytes.Equal(retained, snapshot.State) {
				t.Fatal("backup changed retained Yjs bytes carrying prototype links")
			}
			assertBackupLinks(t, "restored history", designBackupLinks(t, "restored history", retained), wantSnapshot)
		})
	}
}
