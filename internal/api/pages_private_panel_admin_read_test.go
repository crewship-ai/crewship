package api

import (
	"encoding/json"
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
// The fix gives the reads their own resolver (resolvePanelForRead) and keeps
// dispatch, application action status and the restricted catalog on the
// strict one.

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
	// The publication reviewed the routine as it is now, so a dispatch-capable
	// listing with ?publication= carries routine_changed_since_publication.
	checks := `{"routine_definitions":{"` + pageActionRoutine + `":"` + pageRoutineDigest("{}") + `"}}`
	execOrFatal(t, h.db, `INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,1,'panel-read-build',1,?,'synthetic-commit',?,?,?,'2026-11-03T00:00:00Z')`, pageID, digest, digest, spec, checks)
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
	case "panel-actions", "panel-actions-publication":
		target := "/api/v1/pages/" + pageActionSlug + "/panels/" + pageActionPanel + "/actions"
		if surface == "panel-actions-publication" {
			target += "?publication=1"
		}
		r := pagesRequest(t, http.MethodGet, target, ws, user, role, "")
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
	case "action-status":
		t.Fatalf("action-status needs a pending id; use privatePanelStatus")
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

// privatePanelStatus reads GET /pages/{slug}/application/actions/{pendingId}.
func privatePanelStatus(t *testing.T, h *PageHandler, ws, user, role, pendingID string) *httptest.ResponseRecorder {
	t.Helper()
	out := httptest.NewRecorder()
	r := pagesRequest(t, http.MethodGet, "/api/v1/pages/"+pageActionSlug+"/application/actions/"+pendingID, ws, user, role, "")
	r.SetPathValue("slug", pageActionSlug)
	r.SetPathValue("pendingId", pendingID)
	h.ApplicationActionStatus(out, r)
	return out
}

func privatePanelPendingID(t *testing.T, got *httptest.ResponseRecorder) string {
	t.Helper()
	var wire struct {
		PendingID string `json:"pending_id"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &wire); err != nil || wire.PendingID == "" {
		t.Fatalf("dispatch receipt without pending_id: %s %v", got.Body, err)
	}
	return wire.PendingID
}

// privatePanelLeaks reports whether a refused response carries any of the
// private Page's panel content: the stored payload or a declared action.
func privatePanelLeaks(body string) bool {
	for _, marker := range []string{"private-panel-output", "Restart API", pageActionRoutine, "Flotila"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

type privatePanelReader struct {
	name, role string
	crew       bool
	custodian  bool
}

var privatePanelReaders = []privatePanelReader{
	{name: "custodian MEMBER", role: "MEMBER", crew: true, custodian: true},
	{name: "ADMIN outside panel crew", role: "ADMIN"},
	{name: "ADMIN in panel crew", role: "ADMIN", crew: true},
	{name: "OWNER non-custodian", role: "OWNER"},
	{name: "MEMBER in panel crew", role: "MEMBER", crew: true},
	{name: "MEMBER outside panel crew", role: "MEMBER"},
}

// privatePanelSeedReader returns the user id acting as this reader.
func privatePanelSeedReader(t *testing.T, h *PageHandler, ws, custodian string, reader privatePanelReader) string {
	t.Helper()
	if reader.custodian {
		return custodian
	}
	user := "panel-reader"
	pagesSeedUser(t, h, ws, user, user+"@example.test", reader.role)
	if reader.crew {
		pagesJoinCrew(t, h, user, "crew-lookout")
	}
	return user
}

// The #4867 matrix: every reader of another member's private Page against
// every panel surface, with the status and the number of
// artifact.private_owner_accessed rows each request wrote.
//
//   - Reads (page, panel-actions, panel-history): the PRD §5 audience
//     (custodian, OWNER, ADMIN) gets 200; every non-custodian read writes
//     exactly one audit row; a MEMBER, in the panel's crew or not, gets the
//     same 404 as a panel that does not exist, and no audit.
//   - panel-actions advertises can_dispatch only to the dispatch audience,
//     and the application preflight (?publication=) annotates routines only
//     for it.
//   - Writes (dispatch, application dispatch) and application action status
//     stay with custody: ADMIN and MEMBER get 404 with nothing queued. The
//     custodian (a plain MEMBER here) passes the panel gate and is stopped by
//     the dispatch role floor (403), and reads its own run's status. A
//     non-custodian OWNER is in the custody audience (privatePageCustodian)
//     and dispatches: 202, one queued run, its own status 200. That OWNER
//     behaviour is pinned here as current behaviour, not endorsed; changing it
//     is an owner decision (#4867).
func TestPrivatePagePanelReadAudience(t *testing.T) {
	type want struct {
		code, audits int
	}
	for _, reader := range privatePanelReaders {
		readAllowed := reader.custodian || reader.role == "OWNER" || reader.role == "ADMIN"
		custody := reader.custodian || reader.role == "OWNER"
		readAudits := 0
		if readAllowed && !reader.custodian {
			readAudits = 1
		}
		read := want{404, 0}
		if readAllowed {
			read = want{200, readAudits}
		}
		t.Run(reader.name, func(t *testing.T) {
			h, ws, custodian, id := privatePanelFixture(t)
			user := privatePanelSeedReader(t, h, ws, custodian, reader)
			for _, surface := range []string{"page", "panel-actions", "panel-actions-publication", "panel-history"} {
				before := adminReadAudits(t, h, auditActionPrivateArtifactRead, user, id)
				got := privatePanelCall(t, h, ws, user, reader.role, surface)
				audits := adminReadAudits(t, h, auditActionPrivateArtifactRead, user, id) - before
				t.Logf("%s %s: HTTP%d audits=%d", reader.name, surface, got.Code, audits)
				if got.Code != read.code || audits != read.audits {
					t.Errorf("%s: %s HTTP%d audits=%d, want HTTP%d audits=%d: %s", reader.name, surface, got.Code, audits, read.code, read.audits, got.Body)
				}
				if got.Code != 200 {
					if privatePanelLeaks(got.Body.String()) {
						t.Errorf("%s: refused %s leaked panel content: %s", reader.name, surface, got.Body)
					}
					continue
				}
				if !strings.HasPrefix(surface, "panel-actions") {
					continue
				}
				var listing struct {
					CanDispatch *bool `json:"can_dispatch"`
					Actions     []struct {
						ID             string `json:"id"`
						RoutineChanged *bool  `json:"routine_changed_since_publication"`
					} `json:"actions"`
				}
				if err := json.Unmarshal(got.Body.Bytes(), &listing); err != nil || listing.CanDispatch == nil {
					t.Fatalf("%s: %s listing: %v %s", reader.name, surface, err, got.Body)
				}
				if *listing.CanDispatch != custody {
					t.Errorf("%s: %s can_dispatch=%v, want %v", reader.name, surface, *listing.CanDispatch, custody)
				}
				annotated := false
				for _, a := range listing.Actions {
					annotated = annotated || a.RoutineChanged != nil
				}
				if wantAnnotated := custody && surface == "panel-actions-publication"; annotated != wantAnnotated {
					t.Errorf("%s: %s routine annotation=%v, want %v: %s", reader.name, surface, annotated, wantAnnotated, got.Body)
				}
			}
		})
		for _, surface := range []string{"dispatch", "application-dispatch"} {
			t.Run(reader.name+"/"+surface, func(t *testing.T) {
				h, ws, custodian, id := privatePanelFixture(t)
				user := privatePanelSeedReader(t, h, ws, custodian, reader)
				got := privatePanelCall(t, h, ws, user, reader.role, surface)
				queued := pagesPendingRows(t, h)
				audits := adminReadAudits(t, h, auditActionPrivateArtifactRead, user, id)
				t.Logf("%s %s: HTTP%d queued=%d audits=%d", reader.name, surface, got.Code, queued, audits)
				if audits != 0 {
					t.Errorf("%s: %s wrote %d private-read audits; dispatch is not a read", reader.name, surface, audits)
				}
				if !custody {
					if got.Code != 404 || queued != 0 || privatePanelLeaks(got.Body.String()) {
						t.Errorf("%s: %s HTTP%d queued=%d, want 404 and nothing queued: %s", reader.name, surface, got.Code, queued, got.Body)
					}
				} else if reader.custodian {
					// The custodian here is a plain MEMBER: the panel gate admits
					// it, the dispatch role floor (MANAGER+) does not.
					if got.Code != 403 || queued != 0 {
						t.Errorf("%s: %s HTTP%d queued=%d, want the role floor's 403: %s", reader.name, surface, got.Code, queued, got.Body)
					}
				} else if reader.role == "OWNER" {
					// Pinned current behaviour (owner decision pending, #4867).
					if got.Code != 202 || queued != 1 {
						t.Errorf("%s: %s HTTP%d queued=%d, want the pinned 202 with one queued run: %s", reader.name, surface, got.Code, queued, got.Body)
					}
				}
				if surface != "application-dispatch" {
					return
				}
				// Application action status: a caller reads only the status of
				// its own dispatch, through the strict resolver. A caller who
				// could not dispatch asks for the custodian's run (dispatched
				// with a temporary OWNER role to pass the role floor).
				var pendingID string
				if got.Code == 202 {
					pendingID = privatePanelPendingID(t, got)
				} else {
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='OWNER' WHERE workspace_id=? AND user_id=?`, ws, custodian)
					own := privatePanelCall(t, h, ws, custodian, "OWNER", surface)
					if own.Code != 202 {
						t.Fatalf("custodian application dispatch HTTP%d: %s", own.Code, own.Body)
					}
					pendingID = privatePanelPendingID(t, own)
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, custodian)
				}
				before := adminReadAudits(t, h, auditActionPrivateArtifactRead, user, id)
				status := privatePanelStatus(t, h, ws, user, reader.role, pendingID)
				statusAudits := adminReadAudits(t, h, auditActionPrivateArtifactRead, user, id) - before
				t.Logf("%s action-status: HTTP%d audits=%d", reader.name, status.Code, statusAudits)
				wantStatus := 404
				if got.Code == 202 || reader.custodian {
					wantStatus = 200
				}
				if status.Code != wantStatus || statusAudits != 0 {
					t.Errorf("%s: action-status HTTP%d audits=%d, want HTTP%d audits=0: %s", reader.name, status.Code, statusAudits, wantStatus, status.Body)
				}
			})
		}
	}
}

// A private panel read is released only after its audit row is written. When
// the audit insert fails, the ADMIN gets an error and none of the content.
func TestPrivatePagePanelReadFailsClosedWithoutAudit(t *testing.T) {
	h, ws, _, id := privatePanelFixture(t)
	admin := "panel-admin"
	pagesSeedUser(t, h, ws, admin, admin+"@example.test", "ADMIN")
	execOrFatal(t, h.db, `CREATE TRIGGER fail_private_read_audit BEFORE INSERT ON audit_logs WHEN NEW.action='`+auditActionPrivateArtifactRead+`' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	for _, surface := range []string{"page", "panel-actions", "panel-actions-publication", "panel-history"} {
		got := privatePanelCall(t, h, ws, admin, "ADMIN", surface)
		if got.Code < 500 || privatePanelLeaks(got.Body.String()) {
			t.Errorf("%s with a failing audit: HTTP%d, want 5xx and no content: %s", surface, got.Code, got.Body)
		}
	}
	if n := adminReadAudits(t, h, auditActionPrivateArtifactRead, admin, id); n != 0 {
		t.Fatalf("failing audit left %d rows", n)
	}
	execOrFatal(t, h.db, `DROP TRIGGER fail_private_read_audit`)
	if got := privatePanelCall(t, h, ws, admin, "ADMIN", "panel-actions"); got.Code != 200 {
		t.Fatalf("calibration after the audit recovers: HTTP%d %s", got.Code, got.Body)
	}
}

// Read authority follows live membership: an ADMIN demoted to MEMBER gets
// the same 404 as any non-custodian MEMBER, also while a request still
// carries the old role, and writes no audit.
func TestPrivatePagePanelReadEndsWithAdminRole(t *testing.T) {
	h, ws, _, id := privatePanelFixture(t)
	admin := "panel-admin"
	pagesSeedUser(t, h, ws, admin, admin+"@example.test", "ADMIN")
	pagesJoinCrew(t, h, admin, "crew-lookout")
	for _, surface := range []string{"panel-actions", "panel-history"} {
		if got := privatePanelCall(t, h, ws, admin, "ADMIN", surface); got.Code != 200 {
			t.Fatalf("ADMIN %s before demotion: HTTP%d %s", surface, got.Code, got.Body)
		}
	}
	before := adminReadAudits(t, h, auditActionPrivateArtifactRead, admin, id)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, admin)
	for _, contextRole := range []string{"MEMBER", "ADMIN"} {
		for _, surface := range []string{"page", "panel-actions", "panel-history"} {
			got := privatePanelCall(t, h, ws, admin, contextRole, surface)
			if got.Code != 404 || privatePanelLeaks(got.Body.String()) {
				t.Errorf("demoted ADMIN (request role %s) %s: HTTP%d, want 404 without content: %s", contextRole, surface, got.Code, got.Body)
			}
		}
	}
	if n := adminReadAudits(t, h, auditActionPrivateArtifactRead, admin, id) - before; n != 0 {
		t.Fatalf("refused reads after demotion wrote %d audits", n)
	}
}
