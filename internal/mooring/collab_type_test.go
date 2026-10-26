package mooring

import (
	"errors"
	"strings"
	"testing"

	sqlitecodes "modernc.org/sqlite/lib"
)

func TestCollaborationPageRejectsLegacyContentAttachments(t *testing.T) {
	s, _, _, _, _ := batchFixture(t)
	// Satisfy the legacy personal-Page guards and required columns so rejection
	// proves the collaboration fence, regardless of SQLite trigger ordering.
	if _, err := s.db.Exec(`UPDATE pages SET private_audience=1 WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table, insert string
		control       bool
	}{
		{"documents", `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('w',?,1,'{}','{}')`, true},
		{"slides_heads", `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES('w',?,1,1,1,'{}','fixture')`, true},
		{"gridcraft_heads", `INSERT INTO gridcraft_heads(workspace_id,page_id,schema_version,head_rev,generation,snapshot) VALUES('w',?,1,1,1,'{}')`, true},
		{"mooring_documents", `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,snapshot) VALUES('w',?,'fixture',1,'{}')`, true},
		{"page_panels", `INSERT INTO page_panels(page_id) VALUES(?)`, false},
		{"page_project_drafts", `INSERT INTO page_project_drafts(page_id) VALUES(?)`, false},
		{"page_project_live", `INSERT INTO page_project_live(page_id) VALUES(?)`, false},
	} {
		t.Run(tc.table, func(t *testing.T) {
			if tc.control {
				// The same row is valid on an empty personal Page. In particular,
				// a missing workspace or NOT NULL field must not supply the refusal.
				control := "empty-" + tc.table
				if _, err := s.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES(?,'w',?,'Empty','u',1,'{}')`, control, control); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(tc.insert, control); err != nil {
					t.Fatalf("valid legacy row on empty personal Page refused: %v", err)
				}
			}
			_, err := s.db.Exec(tc.insert, "p")
			var constraint interface{ Code() int }
			if !errors.As(err, &constraint) || constraint.Code() != sqlitecodes.SQLITE_CONSTRAINT_TRIGGER || !strings.Contains(err.Error(), "Page already contains collaboration content") {
				t.Errorf("missing collaboration type fence: %v", err)
			}
			var rows int
			if err := s.db.QueryRow("SELECT count(*) FROM " + tc.table + " WHERE page_id='p'").Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Errorf("rejected legacy attachment persisted %d rows", rows)
			}
		})
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
