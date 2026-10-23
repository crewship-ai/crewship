package mooring

import (
	"strings"
	"testing"
)

func TestCollaborationPageRejectsLegacyContentAttachments(t *testing.T) {
	s, _, _, _, _ := batchFixture(t)
	for _, table := range []string{"documents", "slides_heads", "gridcraft_heads", "mooring_documents", "page_panels", "page_project_drafts", "page_project_live"} {
		_, err := s.db.Exec("INSERT INTO " + table + "(page_id) VALUES('p')")
		if err == nil || !strings.Contains(err.Error(), "Page already contains collaboration content") {
			t.Errorf("%s: missing collaboration type fence: %v", table, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE mooring_collab_documents SET application_id='other' WHERE page_id='p'`); err == nil {
		t.Fatal("saved application identity was rebound")
	}
	if _, err := s.db.Exec(`UPDATE pages SET workspace_id='other' WHERE id='p'`); err == nil {
		t.Fatal("saved page scope was rebound")
	}
}

func TestCollaborationInitializationRejectsLegacyDocumentAndWrongScope(t *testing.T) {
	_, db, _, _ := ownerFixture(t)
	if _, err := db.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('w','p',1,'{}','{}')`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('p','w','wordcraft','0','e30=')`)
	if err == nil || !strings.Contains(err.Error(), "Collaboration requires an empty human-owned Page in its workspace") {
		t.Fatal("legacy document accepted as collaboration", err)
	}
	if _, err = db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('other','Other','other')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('q','w','q','Q','u','{}')`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('q','other','wordcraft','0','e30=')`)
	if err == nil || !strings.Contains(err.Error(), "Collaboration requires an empty human-owned Page in its workspace") {
		t.Fatal("cross-workspace collaboration identity accepted", err)
	}
}
