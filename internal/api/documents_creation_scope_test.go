package api

import (
	"context"
	"testing"
	"time"
)

func TestDocumentCreationScopeRejectsSelectedOriginsAndMalformedSchemas(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		allowed   bool
	}{
		{"minimum", "", true},
		{"missing principal origin column", `DROP TRIGGER slides_principal_origin_immutable; ALTER TABLE artifact_run_principals DROP COLUMN slides_request_origin`, false},
		{"missing principal", `DELETE FROM artifact_run_principals`, false},
		{"Slides origin", `UPDATE artifact_run_principals SET slides_request_origin='selected'`, false},
		{"empty nonnull origin", `UPDATE artifact_run_principals SET slides_request_origin=''`, false},
		{"empty Slides", `CREATE TABLE slides_requests(id TEXT,page_id TEXT,original_run_id TEXT)`, true},
		{"different Slides run", `CREATE TABLE slides_requests(id TEXT,page_id TEXT,original_run_id TEXT); INSERT INTO slides_requests VALUES('selected','other-page','other-run')`, true},
		{"selected Slides run", `CREATE TABLE slides_requests(id TEXT,page_id TEXT,original_run_id TEXT); INSERT INTO slides_requests VALUES('selected','other-page','principal-run')`, false},
		{"empty Documents", `CREATE TABLE document_requests(id TEXT,page_id TEXT,run_id TEXT)`, true},
		{"selected Document run", `CREATE TABLE document_requests(id TEXT,page_id TEXT,run_id TEXT); INSERT INTO document_requests VALUES('selected','other-page','principal-run')`, false},
		{"legacy delegation", `CREATE TABLE page_artifact_run_delegations(page_id TEXT,run_id TEXT); INSERT INTO page_artifact_run_delegations VALUES('other-page','principal-run')`, true},
		{"unselected extended delegation", `CREATE TABLE page_artifact_run_delegations(page_id TEXT,run_id TEXT,slides_request_id TEXT,document_request_id TEXT); INSERT INTO page_artifact_run_delegations VALUES('other-page','principal-run',NULL,NULL)`, true},
		{"Slides delegation", `CREATE TABLE page_artifact_run_delegations(page_id TEXT,run_id TEXT,slides_request_id TEXT); INSERT INTO page_artifact_run_delegations VALUES('other-page','principal-run','selected')`, false},
		{"Document delegation", `CREATE TABLE page_artifact_run_delegations(page_id TEXT,run_id TEXT,document_request_id TEXT); INSERT INTO page_artifact_run_delegations VALUES('other-page','principal-run','selected')`, false},
		{"missing Slides columns", `CREATE TABLE slides_requests(id TEXT)`, false},
		{"missing Document columns", `CREATE TABLE document_requests(run_id TEXT)`, false},
		{"missing delegation columns", `CREATE TABLE page_artifact_run_delegations(run_id TEXT)`, false},
		{"view shadows table", `CREATE VIEW slides_requests AS SELECT '' AS id,'' AS page_id,'' AS original_run_id`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newArtifactPrincipalFixture(t)
			f.admit(t)
			if tc.sql != "" {
				mustExec(t, f.rig.db, tc.sql)
			}
			if err := f.pages.checkDocumentCreationRunScope(t.Context(), &f.a); (err == nil) != tc.allowed {
				t.Fatalf("scope allowed=%t: %v", tc.allowed, err)
			}
		})
	}
}

func TestDocumentCreationScopeReadsWriterTransaction(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.admit(t)
	f.rig.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := f.rig.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if err := f.pages.checkDocumentCreationRunScope(ctx, &f.a); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifact_run_principals SET slides_request_origin='selected-in-fence'`); err != nil {
		t.Fatal(err)
	}
	if err := f.pages.checkDocumentCreationRunScope(ctx, &f.a); err == nil {
		t.Fatal("scope ignored uncommitted selected origin")
	}
}
