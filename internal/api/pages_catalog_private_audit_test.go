package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Catalog titles are private metadata too. OWNER custody must be audited
// before the index releases another person's private artifact.
func TestPagesCatalogPrivateOwnerAudit(t *testing.T) {
	h, _, _, workspace, owner := newPagesFixture(t)
	pagesSeedUser(t, h, workspace, "catalog-author", "catalog-author@example.test", "MEMBER")
	page := pagesCreate(t, h, workspace, owner, "private-catalog-title")
	execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='catalog-author',private_audience=1 WHERE id=?`, page["id"])
	request := pagesRequest(t, "GET", "/api/v1/pages", workspace, owner, "OWNER", "")
	response := httptest.NewRecorder()
	h.List(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "private-catalog-title") {
		t.Fatalf("OWNER calibration: HTTP %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id=? AND entity_id=?`, owner, page["id"]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("private title released without one durable audit: got %d", count)
	}
	execOrFatal(t, h.db, `CREATE TRIGGER reject_catalog_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	refused := httptest.NewRecorder()
	h.List(refused, request)
	if refused.Code != 500 || strings.Contains(refused.Body.String(), "private-catalog-title") {
		t.Errorf("failed audit disclosed private catalog title: HTTP %d %s", refused.Code, refused.Body.String())
	}
}
