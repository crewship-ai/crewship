package api

import (
	"database/sql"
	"testing"
)

// Complete a deliberately versioned SQL fixture with its actual saved body.
// Missing-archive negative tests intentionally do not call this helper.
func archiveLegacyFixtureHead(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	result, err := db.ExecContext(t.Context(), `INSERT INTO pipeline_versions
 (id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint)
 SELECT 'fixture-archive-'||id||'-'||head_version,id,head_version,definition_json,definition_hash,'system','fixture','legacy-trusted:v1'
 FROM pipelines WHERE id=? AND head_version>0`, id)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("archive fixture: affected=%d error=%v", n, err)
	}
}
