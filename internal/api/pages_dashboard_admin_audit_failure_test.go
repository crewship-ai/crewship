package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Administrator access to unshared Dashboard data must fail closed at the
// response boundary when its audit cannot be durably recorded. Calibration
// proves both the collection metadata and the row content would otherwise
// be readable, so a disabled route cannot make the privacy assertions pass.
func TestDashboardAdministratorReadAuditFailureReleasesNothing(t *testing.T) {
	const collection = "private-sales-canary"
	const rowID = "private-row-canary"
	const value = "private-value-canary"
	const admin = "dashboard-audit-admin"
	for _, private := range []bool{true, false} {
		t.Run(fmt.Sprintf("private=%t", private), func(t *testing.T) {
			h, ws, owner, call := artifactProjectFixture(t)
			t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
			body := `{"slug":"three-screens","name":"Audit Dashboard",` + strings.TrimPrefix(dashboardBody(0, collection), "{")
			if got := call(owner, http.MethodPost, "artifacts", body); got.Code != http.StatusCreated {
				t.Fatalf("create Dashboard: HTTP %d: %s", got.Code, got.Body)
			}
			batch := fmt.Sprintf(`{"base_version":1,"documents":[{"id":%q,"ifVersion":0,"value":{"secret":%q}}]}`, rowID, value)
			if got := dashboardRequest(t, h, ws, owner, http.MethodPost, collection, "", batch); got.Code != http.StatusOK {
				t.Fatalf("seed Dashboard data: HTTP %d: %s", got.Code, got.Body)
			}
			var page string
			if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&page); err != nil {
				t.Fatal(err)
			}
			mustExec(t, h.db, `UPDATE pages SET private_audience=? WHERE id=?`, private, page)
			pagesSeedUser(t, h, ws, admin, "dashboard-audit-admin@example.test", "ADMIN")
			token := mintTokenFor(t, h.db, admin, "dashboard-audit-admin")
			action := auditActionWorkspaceAdminArtifactRead
			if private {
				action = auditActionPrivateArtifactRead
			}
			read := func(metadata bool) *httptest.ResponseRecorder {
				path := "/api/v1/pages/three-screens/artifact/data"
				if !metadata {
					path += "/" + collection
				}
				r := httptest.NewRequest(http.MethodGet, path+"?version=1", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: admin}), ws, "ADMIN")
				r = r.WithContext(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken))
				r.SetPathValue("slug", "three-screens")
				r.SetPathValue("collection", collection)
				w := httptest.NewRecorder()
				if metadata {
					h.ListArtifactData(w, r)
				} else {
					h.GetArtifactData(w, r)
				}
				return w
			}
			audits := func() int {
				t.Helper()
				var count int
				if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE workspace_id=? AND entity_id=? AND user_id=? AND action=?`, ws, page, admin, action).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			for _, metadata := range []bool{true, false} {
				before := audits()
				got := read(metadata)
				if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), collection) || !metadata && (!strings.Contains(got.Body.String(), rowID) || !strings.Contains(got.Body.String(), value)) {
					t.Fatalf("calibrate metadata=%t: HTTP %d: %s", metadata, got.Code, got.Body)
				}
				if n := audits(); n != before+1 {
					t.Fatalf("calibration metadata=%t audit count %d, want %d", metadata, n, before+1)
				}
			}
			for _, fault := range []string{"abort", "ignore", "delete", "rewrite"} {
				t.Run(fault, func(t *testing.T) {
					statement := "SELECT RAISE(ABORT,'synthetic administrator audit failure');"
					timing := "BEFORE"
					switch fault {
					case "ignore":
						statement = "SELECT RAISE(IGNORE);"
					case "delete":
						timing = "AFTER"
						statement = "DELETE FROM audit_logs WHERE id=NEW.id;"
					case "rewrite":
						timing = "AFTER"
						statement = "UPDATE audit_logs SET metadata='{}' WHERE id=NEW.id;"
					}
					mustExec(t, h.db, `CREATE TRIGGER fail_dashboard_admin_audit `+timing+` INSERT ON audit_logs WHEN NEW.user_id='`+admin+`' AND NEW.action='`+action+`' BEGIN `+statement+` END`)
					t.Cleanup(func() { mustExec(t, h.db, `DROP TRIGGER fail_dashboard_admin_audit`) })
					for _, metadata := range []bool{true, false} {
						before := audits()
						got := read(metadata)
						if got.Code != http.StatusInternalServerError {
							t.Errorf("metadata=%t HTTP %d, want 500: %s", metadata, got.Code, got.Body)
						}
						for _, secret := range []string{collection, rowID, value} {
							if strings.Contains(got.Body.String(), secret) {
								t.Errorf("metadata=%t disclosed %q after failed audit", metadata, secret)
							}
						}
						if n := audits(); n != before {
							t.Errorf("failed audit persisted rows: %d before, %d after", before, n)
						}
					}
				})
			}
		})
	}
}
