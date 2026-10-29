package api

import (
	"github.com/crewship-ai/crewship/internal/appengine"
	"testing"
)

func TestReviewMooringCommitBoundary(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"credential_revoked", `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z';`},
		{"snapshot_rewritten", `UPDATE mooring_documents SET snapshot='{"text":"tampered"}';`},
		{"receipt_deleted", `DELETE FROM mooring_receipts;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gridcraftInstanceFixture(t)
			f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
			wantCode(t, f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", `{"app_id":"hello","slug":"review-mooring","name":"Review"}`), 201, "create")
			mustExec(t, f.db, `CREATE TRIGGER review_mooring_boundary AFTER INSERT ON journal_entries WHEN NEW.entry_type='artifact.mooring_committed' BEGIN `+tc.sql+` END`)
			w := f.do(f.boss, "POST", "/api/v1/pages/review-mooring/mooring/commands?workspace_id=ws-old", `{"mutation_id":"one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"expected"}}`)
			if w.Code == 200 {
				t.Errorf("success receipt after %s: %s", tc.name, w.Body.String())
			}
			var rev, n int
			if e := f.db.QueryRow(`SELECT rev FROM mooring_documents`).Scan(&rev); e != nil {
				t.Fatal(e)
			}
			if e := f.db.QueryRow(`SELECT count(*) FROM mooring_receipts`).Scan(&n); e != nil {
				t.Fatal(e)
			}
			if rev != 1 || n != 0 {
				t.Errorf("partial commit rev=%d receipts=%d", rev, n)
			}
		})
	}
}

func TestReviewMooringCreateBoundary(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"credential_revoked", `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z';`},
		{"snapshot_rewritten", `UPDATE mooring_documents SET snapshot='{"text":"tampered"}';`},
		{"owner_rewritten", `UPDATE pages SET private_audience=0 WHERE slug='review-created';`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := gridcraftInstanceFixture(t)
			f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
			mustExec(t, f.db, `CREATE TRIGGER review_create_boundary AFTER INSERT ON journal_entries WHEN NEW.entry_type='artifact.mooring_created' BEGIN `+tc.sql+` END`)
			w := f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", `{"app_id":"hello","slug":"review-created","name":"Review"}`)
			if w.Code == 201 {
				t.Errorf("success after %s: %s", tc.name, w.Body.String())
			}
			var n int
			if e := f.db.QueryRow(`SELECT count(*) FROM pages WHERE slug='review-created'`).Scan(&n); e != nil {
				t.Fatal(e)
			}
			if n != 0 {
				t.Errorf("partial commit pages=%d", n)
			}
		})
	}
}
