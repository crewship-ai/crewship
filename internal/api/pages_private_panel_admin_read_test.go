package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #4867, PRD §5 (owner decision 30. 10.): a workspace OWNER or ADMIN reads
// every artifact, including another member's private Page, and every such
// read is audited; the read never becomes write. The panel READ surfaces of a
// private Page (declared actions, application panel history) resolve the
// caller through resolvePanelForCaller, which loads the Page with the strict
// custodian gate (loadPage -> privatePageCustodian: custodian or OWNER), not
// the read audience (loadPageForRead -> privatePageViewer: custodian, OWNER,
// ADMIN). A non-custodian ADMIN is therefore refused panel reads that the
// detail read (GET /pages/{slug}) already serves it. Panel action DISPATCH
// shares the same resolver and must stay closed to the ADMIN read audience.

// privatePanelFixture is a private Page with one panel owned by crew-lookout
// that declares a call action, a live publication of the current spec, and
// one stored panel payload. The custodian is a plain MEMBER in crew-lookout.
func privatePanelFixture(t *testing.T) (h *PageHandler, ws, custodian, pageID string) {
	t.Helper()
	h, _, ws, custodian = newPageActionFixture(t)
	pagesJoinCrew(t, h, custodian, "crew-lookout")
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, custodian)
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug=?`, ws, pageActionSlug).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE pages SET private_audience=1, owner_user_id=? WHERE id=?`, custodian, pageID)
	var spec, row string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE id=?`, pageID).Scan(&spec); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT id FROM page_panels WHERE page_id=? AND panel_id=?`, pageID, pageActionPanel).Scan(&row); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	execOrFatal(t, h.db, `INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,created_at) VALUES('panel-read-build',?,1,?,'ready','2026-11-03T00:00:00Z')`, pageID, digest)
	execOrFatal(t, h.db, `INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,1,'panel-read-build',1,?,'synthetic-commit',?,?,'{}','2026-11-03T00:00:00Z')`, pageID, digest, digest, spec)
	execOrFatal(t, h.db, `INSERT INTO page_project_live(page_id,version) VALUES(?,1)`, pageID)
	execOrFatal(t, h.db, `INSERT INTO page_panel_data(panel_id,seq,payload_json,produced_at,state) VALUES(?,1,'{"items":[{"name":"private-panel-output","state":"ok"}]}','2026-11-03','ok')`, row)
	return h, ws, custodian, pageID
}

func privatePanelCall(t *testing.T, h *PageHandler, ws, user, role, surface string) *httptest.ResponseRecorder {
	t.Helper()
	out := httptest.NewRecorder()
	switch surface {
	case "page":
		return pagesGetStatus(t, h, ws, user, role, pageActionSlug)
	case "panel-actions":
		r := pagesRequest(t, http.MethodGet, "/api/v1/pages/"+pageActionSlug+"/panels/"+pageActionPanel+"/actions", ws, user, role, "")
		r.SetPathValue("slug", pageActionSlug)
		r.SetPathValue("panelId", pageActionPanel)
		h.ListPanelActions(out, r)
	case "panel-history":
		r := pagesRequest(t, http.MethodGet, "/api/v1/pages/"+pageActionSlug+"/application/panels/"+pageActionPanel+"/history?publication=1", ws, user, role, "")
		r.SetPathValue("slug", pageActionSlug)
		r.SetPathValue("panelId", pageActionPanel)
		h.ApplicationPanelHistory(out, r)
	case "dispatch":
		return pagesDispatch(t, h, ws, user, role, pageActionID, `{"inputs":{"reason":"x"}}`, "")
	case "application-dispatch":
		r := pagesRequest(t, http.MethodPost, fmt.Sprintf("/api/v1/pages/%s/application/actions/%s/%s", pageActionSlug, pageActionPanel, pageActionID), ws, user, role, `{"publication":1,"inputs":{"reason":"x"}}`)
		r.SetPathValue("slug", pageActionSlug)
		r.SetPathValue("panelId", pageActionPanel)
		r.SetPathValue("actionId", pageActionID)
		r.Header.Set("Idempotency-Key", "panel-read-"+user)
		h.DispatchApplicationAction(out, r)
	default:
		t.Fatalf("unknown surface %q", surface)
	}
	return out
}

// Calibration: the custodian reads every panel surface without an audit.
func TestPrivatePagePanelReadCustodianCalibration(t *testing.T) {
	h, ws, custodian, id := privatePanelFixture(t)
	for _, surface := range []string{"page", "panel-actions", "panel-history"} {
		before := adminReadAudits(t, h, auditActionPrivateArtifactRead, custodian, id)
		if got := privatePanelCall(t, h, ws, custodian, "MEMBER", surface); got.Code != 200 {
			t.Errorf("custodian %s HTTP%d want200: %s", surface, got.Code, got.Body)
		}
		if n := adminReadAudits(t, h, auditActionPrivateArtifactRead, custodian, id) - before; n != 0 {
			t.Errorf("custodian %s wrote %d private-access audits, want 0", surface, n)
		}
	}
}

// (a)(b)(c): panel READ surfaces of another member's private Page per reader.
// OWNER/ADMIN: 200 and exactly one artifact.private_owner_accessed audit.
// MEMBER (in the panel's crew, or not): 404, no audit.
func TestPrivatePagePanelReadAudience(t *testing.T) {
	cases := []struct {
		name, role string
		crew       bool
		allowed    bool
	}{
		{"ADMIN non-custodian", "ADMIN", false, true},
		{"ADMIN non-custodian in panel crew", "ADMIN", true, true},
		{"OWNER non-custodian", "OWNER", false, true},
		{"MEMBER non-custodian in panel crew", "MEMBER", true, false},
		{"MEMBER non-custodian outside panel crew", "MEMBER", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, _, id := privatePanelFixture(t)
			reader := "panel-reader"
			pagesSeedUser(t, h, ws, reader, reader+"@example.test", tc.role)
			if tc.crew {
				pagesJoinCrew(t, h, reader, "crew-lookout")
			}
			for _, surface := range []string{"page", "panel-actions", "panel-history"} {
				before := adminReadAudits(t, h, auditActionPrivateArtifactRead, reader, id)
				got := privatePanelCall(t, h, ws, reader, tc.role, surface)
				audits := adminReadAudits(t, h, auditActionPrivateArtifactRead, reader, id) - before
				t.Logf("%s %s: HTTP%d audits=%d", tc.name, surface, got.Code, audits)
				if !tc.allowed {
					if got.Code != 404 || strings.Contains(got.Body.String(), "private-panel-output") || strings.Contains(got.Body.String(), "Restart API") {
						t.Errorf("%s: %s HTTP%d want404 without content: %s", tc.name, surface, got.Code, got.Body)
					}
					if audits != 0 {
						t.Errorf("%s: %s refused read wrote %d audits", tc.name, surface, audits)
					}
					continue
				}
				if got.Code != 200 {
					t.Errorf("%s: %s HTTP%d want200: %s", tc.name, surface, got.Code, got.Body)
				}
				if audits != 1 {
					t.Errorf("%s: %s released private panel output with %d audits, want exactly one", tc.name, surface, audits)
				}
			}
		})
	}
}

// (d): the panel read audience is not a dispatch audience. A non-custodian
// ADMIN is refused every panel action dispatch with 404 and nothing queued.
// The OWNER is in the private custody (write) audience by design
// (privatePageCustodian); its dispatch result is recorded, not asserted.
func TestPrivatePagePanelDispatchStaysWithCustody(t *testing.T) {
	cases := []struct {
		name, role string
		crew       bool
		assertDeny bool
	}{
		{"ADMIN non-custodian", "ADMIN", false, true},
		{"ADMIN non-custodian in panel crew", "ADMIN", true, true},
		{"MEMBER non-custodian in panel crew", "MEMBER", true, true},
		{"OWNER non-custodian", "OWNER", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, surface := range []string{"dispatch", "application-dispatch"} {
				h, ws, _, _ := privatePanelFixture(t)
				reader := "panel-operator"
				pagesSeedUser(t, h, ws, reader, reader+"@example.test", tc.role)
				if tc.crew {
					pagesJoinCrew(t, h, reader, "crew-lookout")
				}
				got := privatePanelCall(t, h, ws, reader, tc.role, surface)
				queued := pagesPendingRows(t, h)
				t.Logf("%s %s: HTTP%d queued=%d", tc.name, surface, got.Code, queued)
				if tc.assertDeny && (got.Code != 404 || queued != 0) {
					t.Errorf("%s: %s HTTP%d queued=%d want404 and nothing queued: %s", tc.name, surface, got.Code, queued, got.Body)
				}
			}
		})
	}
}
