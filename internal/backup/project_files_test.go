package backup_test

import (
	"bytes"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestProjectFilesBackupPreservesBytesAndExcludesCapabilities(t *testing.T) {
	db := openMigratedDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('pf-owner','pf-owner@backup.test'),('pf-human','pf-human@backup.test'),('pf-outsider','outsider@backup.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('pf-w','Files','pf-w')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pf-mo','pf-w','pf-owner','OWNER'),('pf-mh','pf-w','pf-human','MANAGER')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('pf-p','pf-w','Project','pf-p')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	store := access.Store{DB: db}
	content := []byte{0, 255, 10, 13, 128, 'P', 'R', 'I', 'V', 'A', 'T', 'E'}
	v, err := store.PutProjectFile(t.Context(), "pf-owner", "pf-w", "pf-p", access.ProjectFileWrite{Name: "data.bin"}, content)
	if err != nil {
		t.Fatal(err)
	}
	m, err := store.Membership(t.Context(), "pf-human", "pf-w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "pf-owner", "pf-human", "pf-w", "restricted", m, []access.Right{{Kind: "project", ID: "pf-p", Operation: "read"}}); err != nil {
		t.Fatal(err)
	}
	// Both identities exist only as workspace members: no crew and no chats.
	first, err := backup.DumpWorkspace(t.Context(), db, "pf-w")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tables["users"]) != 2 {
		t.Fatal("workspace-only users missing or unrelated identity exported")
	}
	firstClone := openMigratedDB(t)
	if err = backup.RestoreDump(t.Context(), firstClone, first); err != nil {
		t.Fatal(err)
	}
	if _, b, e := (access.Store{DB: firstClone}).ReadProjectFile(t.Context(), "pf-human", "pf-w", "pf-p", v.ID); e != nil || !bytes.Equal(b, content) {
		t.Fatalf("workspace-only member file restore %v", e)
	}
	for _, q := range []string{
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('pf-c','pf-w','Crew','pf-c')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('pf-a','pf-w','pf-c','Agent','pf-a')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('pf-chat','pf-w','pf-a','pf-human','private')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	m, err = store.Membership(t.Context(), "pf-human", "pf-w")
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "agent", ID: "pf-a", Operation: "run"}, {Kind: "project", ID: "pf-p", Operation: "read"}}
	if _, err = store.Replace(t.Context(), "pf-owner", "pf-human", "pf-w", "restricted", m, rights); err != nil {
		t.Fatal(err)
	}
	h, _, err := store.Admit(t.Context(), "pf-human", "pf-w", "pf-a", "pf-chat", "", rights[1:])
	if err != nil {
		t.Fatal(err)
	}
	if err = store.BindProjectFileInputs(t.Context(), h, []string{v.ID}); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), db, "pf-w")
	if err != nil {
		t.Fatal(err)
	}
	if len(dump.Tables["attempt_project_inputs"]) != 0 {
		t.Fatal("restorable input capability exported")
	}
	if len(dump.Tables["project_file_blobs"]) != 1 {
		t.Fatal("source bytes missing")
	}
	clone := openMigratedDB(t)
	if err = backup.RestoreDump(t.Context(), clone, dump); err != nil {
		t.Fatal(err)
	}
	restored := access.Store{DB: clone}
	got, data, err := restored.ReadProjectFile(t.Context(), "pf-owner", "pf-w", "pf-p", v.ID)
	if err != nil || got.SHA256 != v.SHA256 || !bytes.Equal(data, content) {
		t.Fatalf("source byte/hash roundtrip %v %#v", err, got)
	}
	if _, err = restored.Resolve(t.Context(), h); err == nil {
		t.Fatal("copied attempt resumed")
	}
}
