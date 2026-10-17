package api

import (
	"database/sql"
	"strings"
	"testing"
)

// These optional schemas and associations match the existing creation scope
// tests. The trusted consent binder must enforce that boundary before binding,
// including when an installed optional schema cannot be understood safely.
func TestDocumentCreationAdmissionBindingEnforcesStandaloneScope(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		allowed   bool
	}{
		{"minimum without optional tables", "", true},
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
			f, intent := documentCreationAdmissionFixture(t)
			if tc.sql != "" {
				// The minimum case retains the real installed schema. These isolated
				// variants deliberately reconstruct an optional future/legacy shape.
				if strings.Contains(tc.sql, "CREATE TABLE page_artifact_run_delegations(") {
					mustExec(t, f.rig.db, `DROP TABLE IF EXISTS page_artifact_run_delegations`)
				}
				mustExec(t, f.rig.db, tc.sql)
			}
			var originalExpiry string
			if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&originalExpiry); err != nil {
				t.Fatal(err)
			}
			f.rig.db.SetMaxOpenConns(1)
			if err := bindCreation(f, intent); (err == nil) != tc.allowed {
				t.Errorf("binding allowed=%t want%t: %v", err == nil, tc.allowed, err)
			}
			var run, expiry sql.NullString
			if err := f.rig.db.QueryRow(`SELECT run_id,expires_at FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&run, &expiry); err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				if !run.Valid || run.String != f.a.RunID || !expiry.Valid || expiry.String != originalExpiry {
					t.Errorf("valid standalone consent lost original binding/expiry: run=%v expiry=%v", run, expiry)
				}
			} else if run.Valid || expiry.Valid {
				t.Error("rejected scope retained a consent binding or expiry")
			}
		})
	}
}
