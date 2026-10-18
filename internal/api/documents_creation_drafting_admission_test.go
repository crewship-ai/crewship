package api

import (
	"database/sql"
	"fmt"
	"testing"
)

func TestDocumentCreationDraftingPurposeBinding(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f, intent := documentCreationAdmissionFixture(t, enabled)
			var allowed bool
			var generation, revision sql.NullInt64
			if err := f.rig.db.QueryRow(`SELECT drafting_allowed,draft_generation,draft_rev FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&allowed, &generation, &revision); err != nil {
				t.Fatal(err)
			}
			if allowed != enabled || generation.Valid || revision.Valid {
				t.Fatal("admission changed purpose or bound an uncreated document")
			}
			changed := intent
			changed.Drafting = !enabled
			if err := bindCreation(f, changed); err == nil {
				t.Fatal("different drafting purpose acquired the original consent")
			}
			var bound sql.NullString
			if err := f.rig.db.QueryRow(`SELECT run_id FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&bound); err != nil || bound.Valid {
				t.Fatalf("rejected purpose changed run binding: %v", err)
			}
			if err := bindCreation(f, intent); err != nil {
				t.Fatal(err)
			}
			if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET drafting_allowed=? WHERE message_id=?`, !enabled, f.a.MessageID); err == nil {
				t.Fatal("original drafting purpose was mutable")
			}
			if err := bindCreation(f, intent); err != nil {
				t.Fatalf("identical binding: %v", err)
			}
		})
	}
}

func TestDocumentCreationDraftingCursorConstraints(t *testing.T) {
	f, _ := documentCreationAdmissionFixture(t, true)
	if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET draft_generation=0 WHERE message_id=?`, f.a.MessageID); err == nil {
		t.Fatal("partial cursor binding accepted")
	}
	if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET draft_generation=0,draft_rev=0 WHERE message_id=?`, f.a.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET draft_rev=1 WHERE message_id=?`, f.a.MessageID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE document_creation_consents SET draft_generation=1`,
		`UPDATE document_creation_consents SET draft_generation=NULL,draft_rev=NULL`,
		`UPDATE document_creation_consents SET draft_rev=0`,
	} {
		if _, err := f.rig.db.Exec(query); err == nil {
			t.Fatalf("immutable cursor constraint accepted: %s", query)
		}
	}
	legacy, _ := documentCreationAdmissionFixture(t)
	if _, err := legacy.rig.db.Exec(`UPDATE document_creation_consents SET draft_generation=0,draft_rev=0`); err == nil {
		t.Fatal("one-shot consent acquired a draft cursor")
	}
}
