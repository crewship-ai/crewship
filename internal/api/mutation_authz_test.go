package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mutation_authz_test.go — guards the role gate on the previously un-gated
// mutation endpoints (PRD [5]). These endpoints registered as authed(wsCtx(...))
// = auth + workspace membership only, so ANY member (including a VIEWER) could
// deploy crews, drive pipeline-run control-plane, and restore/fork/delete
// checkpoints. Each now requires MANAGER+ (create/update). RED on main: no gate,
// so a VIEWER reaches the handler body and gets a non-403 (404/503/400).
//
// NOTE: this is a regression guard over the specific routes fixed here, mirrored
// on unauth_reachability_test.go — Go's ServeMux exposes no way to iterate
// registered patterns, so a build-failing "every mutation route declares a role"
// invariant needs a walkable route-table refactor (tracked as a follow-up).

func viewerAuthzReq(t *testing.T, userID, wsID, role, method, target, body string, pathvals map[string]string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	for k, v := range pathvals {
		r.SetPathValue(k, v)
	}
	return withWorkspaceUser(r, userID, wsID, role)
}

func TestMutationRoutes_RejectViewer(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := newTestLogger()

	pipes := NewPipelineHandler(db, logger, nil, nil)
	carto := NewCartographerHandler(db, logger)
	tmpl := NewCrewTemplateHandler(db, logger)
	policies := NewCrewPolicyHandler(db, nil, logger)

	call := func(h http.HandlerFunc, method, target, body string, pv map[string]string) int {
		rr := httptest.NewRecorder()
		h(rr, viewerAuthzReq(t, userID, wsID, "VIEWER", method, target, body, pv))
		return rr.Code
	}

	cases := []struct {
		name   string
		h      http.HandlerFunc
		method string
		target string
		body   string
		pv     map[string]string
	}{
		{"crew-template deploy", tmpl.Deploy, "POST", "/x", `{"crew_name":"x"}`, map[string]string{"slug": "t"}},
		{"crew policy put", policies.Put, "PUT", "/x", `{}`, map[string]string{"crewId": "c"}},
		{"pipeline run", pipes.Run, "POST", "/x", `{}`, map[string]string{"slug": "p"}},
		{"pipeline run_batch", pipes.RunBatch, "POST", "/x", `{}`, map[string]string{"slug": "p"}},
		{"pipeline waitpoint approve", pipes.ApproveWaitpoint, "POST", "/x", `{}`, map[string]string{"token": "tok"}},
		{"step-override set", pipes.SetStepOverride, "PUT", "/x", `{}`, map[string]string{"slug": "p", "stepId": "s"}},
		{"step-override delete", pipes.DeleteStepOverride, "DELETE", "/x", "", map[string]string{"slug": "p", "stepId": "s"}},
		{"run replay", pipes.ReplayRun, "POST", "/x", "", map[string]string{"runId": "r"}},
		{"run bulk replay", pipes.BulkReplayRuns, "POST", "/x", `{"run_ids":["r"]}`, nil},
		{"run signal", pipes.SignalRun, "POST", "/x", `{}`, map[string]string{"runId": "r"}},
		{"run metadata", pipes.UpdateRunMetadata, "PATCH", "/x", `{}`, map[string]string{"runId": "r"}},
		{"pending cancel", pipes.CancelPendingRun, "POST", "/x", "", map[string]string{"pendingId": "p"}},
		{"checkpoint create", carto.Create, "POST", "/x", `{}`, map[string]string{"missionId": "m"}},
		{"checkpoint restore", carto.Restore, "POST", "/x", "", map[string]string{"id": "c"}},
		{"checkpoint fork", carto.Fork, "POST", "/x", "", map[string]string{"id": "c"}},
		{"checkpoint delete", carto.Delete, "DELETE", "/x", "", map[string]string{"id": "c"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code := call(c.h, c.method, c.target, c.body, c.pv); code != http.StatusForbidden {
				t.Fatalf("%s as VIEWER = %d, want 403 (endpoint must gate role, not just membership)", c.name, code)
			}
		})
	}
}

// The gate must not over-block: a MANAGER (the intended tier for these
// control-plane mutations) passes the role check and reaches the handler body
// (which then returns a non-403 like 404/503 for the missing test fixtures).
func TestMutationRoutes_AllowManager(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := newTestLogger()
	pipes := NewPipelineHandler(db, logger, nil, nil)
	tmpl := NewCrewTemplateHandler(db, logger)

	call := func(h http.HandlerFunc, method, target, body string, pv map[string]string) int {
		rr := httptest.NewRecorder()
		h(rr, viewerAuthzReq(t, userID, wsID, "MANAGER", method, target, body, pv))
		return rr.Code
	}
	if code := call(tmpl.Deploy, "POST", "/x", `{"crew_name":"x"}`, map[string]string{"slug": "t"}); code == http.StatusForbidden {
		t.Errorf("deploy as MANAGER = 403; MANAGER is the intended tier (must not over-block)")
	}
	if code := call(pipes.UpdateRunMetadata, "PATCH", "/x", `{}`, map[string]string{"runId": "r"}); code == http.StatusForbidden {
		t.Errorf("run metadata as MANAGER = 403; MANAGER is the intended tier (must not over-block)")
	}
}
