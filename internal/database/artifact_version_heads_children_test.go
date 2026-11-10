package database

import (
	"slices"
	"testing"
)

// Every table whose rows hang off artifact_version_heads through a foreign key
// with ON DELETE CASCADE. A migration that rebuilds artifact_version_heads
// (DROP + CREATE, as 20261028202000_artifact_design_yjs.sql does) silently
// deletes every row of these tables unless it copies them aside first. When
// this list changes, check every such rebuild and update it here on purpose.
var artifactVersionHeadsChildren = []string{
	"artifact_data_collections",
	"artifact_data_runs",
	"artifact_design_documents",
	"artifact_library_pins",
	"artifact_library_publications",
}

func TestArtifactVersionHeadsForeignKeyChildrenAreKnown(t *testing.T) {
	db := openMigratedTestDB(t)
	rows, err := db.Query(`SELECT DISTINCT m.name FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f ON f."table" COLLATE NOCASE='artifact_version_heads' WHERE m.type='table' ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, artifactVersionHeadsChildren) {
		t.Fatalf("artifact_version_heads foreign-key children changed:\n got  %v\n want %v\nA rebuild of artifact_version_heads must preserve every child; update the list deliberately.", got, artifactVersionHeadsChildren)
	}
}
