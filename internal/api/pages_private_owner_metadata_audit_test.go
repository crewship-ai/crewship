package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type privateMetadataRoute struct {
	name, path string
	handler    func(*PageHandler, http.ResponseWriter, *http.Request)
}

var privateMetadataRoutes = []privateMetadataRoute{
	{"metadata", "", (*PageHandler).Get},
	{"export", "/export", (*PageHandler).Export},
	{"versions", "/versions", (*PageHandler).ListVersions},
	{"grants", "/grants", (*PageHandler).ListGrants},
	{"page-access", "/access", (*PageHandler).PageAccess},
	{"my-access", "/access/me", (*PageHandler).MyPageAccess},
	{"subject-access", "subject", (*PageHandler).SubjectAccess},
}

func privateMetadataRequest(t *testing.T, h *PageHandler, route privateMetadataRoute, workspace, user, role, slug, subject string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/pages/" + slug + route.path
	if route.path == "subject" {
		path = "/api/v1/pages/access?subject=user:" + subject
	}
	request := pagesRequest(t, "GET", path, workspace, user, role, "")
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	request.SetPathValue("slug", slug)
	out := httptest.NewRecorder()
	route.handler(h, out, request)
	return out
}

func privateMetadataFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, workspace, custodian, _, create := documentCreationFixture(t)
	creationExpect(t, create("private", documentCreationBody), 201)
	var page string
	var private, document int
	if err := h.db.QueryRow(`SELECT p.id,p.private_audience,EXISTS(SELECT 1 FROM documents d WHERE d.page_id=p.id) FROM pages p WHERE p.workspace_id=? AND p.slug='created-doc'`, workspace).Scan(&page, &private, &document); err != nil || private != 1 || document != 1 {
		t.Fatalf("genuine created private Document: %d %d %v", private, document, err)
	}
	// The fixture's other user is a real trusted OWNER; the custodian is MEMBER.
	return h, workspace, custodian, page
}

func privateMetadataAuditCount(t *testing.T, h *PageHandler, workspace, page, actor, custodian string) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE workspace_id=? AND entity_type='page' AND entity_id=? AND user_id=? AND action='artifact.private_owner_accessed' AND json_extract(metadata,'$.owner_user_id')=?`, workspace, page, actor, custodian).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPrivateOwnerMetadataReadAudit(t *testing.T) {
	for _, route := range privateMetadataRoutes {
		t.Run(route.name, func(t *testing.T) {
			h, workspace, custodian, page := privateMetadataFixture(t)
			own := privateMetadataRequest(t, h, route, workspace, custodian, "MEMBER", "created-doc", custodian)
			if own.Code != 200 || !strings.Contains(own.Body.String(), "created-doc") {
				t.Fatalf("custodian healthy read: %d %s", own.Code, own.Body.String())
			}
			if n := privateMetadataAuditCount(t, h, workspace, page, custodian, custodian); n != 0 {
				t.Fatalf("own custody read unexpectedly audited: %d", n)
			}
			other := privateMetadataRequest(t, h, route, workspace, "ptadmin", "OWNER", "created-doc", custodian)
			if other.Code != 200 || !strings.Contains(other.Body.String(), "created-doc") {
				t.Fatalf("OWNER healthy read: %d %s", other.Code, other.Body.String())
			}
			if n := privateMetadataAuditCount(t, h, workspace, page, "ptadmin", custodian); n != 1 {
				t.Errorf("OWNER private metadata released without exact durable audit: count=%d", n)
			}
		})
	}
}

func TestPrivateOwnerMetadataReadAuditFailure(t *testing.T) {
	for _, route := range privateMetadataRoutes {
		t.Run(route.name, func(t *testing.T) {
			for _, fault := range []string{"before-ABORT", "before-IGNORE", "after-DELETE"} {
				t.Run(fault, func(t *testing.T) {
					h, workspace, custodian, page := privateMetadataFixture(t)
					calibration := privateMetadataRequest(t, h, route, workspace, custodian, "MEMBER", "created-doc", custodian)
					if calibration.Code != 200 {
						t.Fatalf("custodian calibration: %d %s", calibration.Code, calibration.Body.String())
					}
					trigger := `CREATE TRIGGER refuse_private_metadata_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'synthetic private audit unavailable'); END`
					if fault == "before-IGNORE" {
						trigger = strings.Replace(trigger, "ABORT,'synthetic private audit unavailable'", "IGNORE", 1)
					}
					if fault == "after-DELETE" {
						trigger = `CREATE TRIGGER refuse_private_metadata_audit AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN DELETE FROM audit_logs WHERE id=NEW.id; END`
					}
					execOrFatal(t, h.db, trigger)
					out := privateMetadataRequest(t, h, route, workspace, "ptadmin", "OWNER", "created-doc", custodian)
					if out.Code != 500 {
						t.Errorf("%s released private metadata when committed audit unavailable: status%d", fault, out.Code)
					}
					for _, canary := range []string{"created-doc", "Created report", page} {
						if strings.Contains(out.Body.String(), canary) {
							t.Errorf("audit failure disclosed private metadata %q", canary)
						}
					}
					if n := privateMetadataAuditCount(t, h, workspace, page, "ptadmin", custodian); n != 0 {
						t.Errorf("audit fault unexpectedly durable: %d", n)
					}
				})
			}
		})
	}
}

func TestOrdinaryMetadataReadAuditCompatibility(t *testing.T) {
	h, _, _, workspace, custodian := newPagesFixture(t)
	slug := "ordinary-metadata-audit"
	pagesCreateWith(t, h, workspace, custodian, pagesReachBody(slug, "lookout"))
	pagesSeedUser(t, h, workspace, "ordinary-owner-reader", "ordinary-owner-reader@example.invalid", "OWNER")
	for _, route := range privateMetadataRoutes {
		t.Run(route.name, func(t *testing.T) {
			out := privateMetadataRequest(t, h, route, workspace, "ordinary-owner-reader", "OWNER", slug, custodian)
			if out.Code != 200 || !strings.Contains(out.Body.String(), slug) {
				t.Fatalf("ordinary read: %d %s", out.Code, out.Body.String())
			}
			var count int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed'`).Scan(&count); err != nil || count != 0 {
				t.Fatal(fmt.Sprintf("ordinary read audit=%d err%v", count, err))
			}
		})
	}
}
