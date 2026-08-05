package api

import (
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// #1763 — two of the four routine MCP tools were dead for in-container agents.
//
// The sidecar authenticates with X-Internal-Token (sidecar/pipelines.go), and
// list_routines / discover_capabilities forwarded to the PUBLIC, JWT-authed
// routes. extractToken never looks at that header, so both answered 401 and an
// agent asked to author a routine could see neither what its crew can reach
// nor what routines already exist — it wrote from memory instead, which is the
// exact failure the capabilities dump exists to prevent.
//
// save_routine and run_routine worked all along because they target
// /api/v1/internal/*. There was simply no internal counterpart for the two
// READ tools.
//
// These drive the whole Router, so the real auth middleware runs. The test
// that shipped with the tools could not see any of this: it mocks the upstream
// with a handler returning 200 for every request, which proves the sidecar
// calls the right PATH and says nothing about whether the server accepts it.
// ---------------------------------------------------------------------------

// TestPublicPipelineRoutes_RejectTheInternalToken pins WHY the internal
// counterparts have to exist. If a future change taught extractToken to read
// X-Internal-Token, this would fail — and that change would hand every holder
// of the shared internal secret a user-equivalent session on the whole public
// API, which is a much larger decision than fixing two tools.
func TestPublicPipelineRoutes_RejectTheInternalToken(t *testing.T) {
	r, wsID := newFenceRouter(t)
	auth := map[string]string{"X-Internal-Token": fenceInternalToken}

	for _, target := range []string{
		"/api/v1/workspaces/" + wsID + "/pipelines",
		"/api/v1/crews/crew-does-not-matter/capabilities?workspace_id=" + wsID,
	} {
		rr := probeInternalFence(t, r, http.MethodGet, target, fenceLoopbackAddr, auth)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with only an internal token: status = %d, want 401 — body: %q",
				target, rr.Code, rr.Body.String())
		}
	}
}

func TestInternalPipelinesList_AcceptsTheSidecarToken(t *testing.T) {
	r, wsID := newFenceRouter(t)
	auth := map[string]string{"X-Internal-Token": fenceInternalToken}

	rr := probeInternalFence(t, r, http.MethodGet,
		"/api/v1/internal/pipelines?workspace_id="+wsID, fenceLoopbackAddr, auth)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /internal/pipelines: status = %d, want 200 — body: %q", rr.Code, rr.Body.String())
	}
	// A fresh workspace has none; the shape still has to be the list shape the
	// public route returns, because the tool hands this straight to a model.
	if body := strings.TrimSpace(rr.Body.String()); !strings.Contains(body, "[") {
		t.Errorf("body = %q, want the pipeline list shape", body)
	}
}

func TestInternalPipelinesList_RequiresAWorkspace(t *testing.T) {
	r, _ := newFenceRouter(t)
	auth := map[string]string{"X-Internal-Token": fenceInternalToken}

	// Without it the handler would list nothing and look like an empty
	// workspace, which reads as "you have no routines" rather than "you asked
	// wrong" — the worst possible answer to give a model.
	rr := probeInternalFence(t, r, http.MethodGet,
		"/api/v1/internal/pipelines", fenceLoopbackAddr, auth)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("GET /internal/pipelines with no workspace_id: status = %d, want 400 — body: %q",
			rr.Code, rr.Body.String())
	}
}

func TestInternalCrewCapabilities_AcceptsTheSidecarToken(t *testing.T) {
	r, wsID := newFenceRouter(t)
	auth := map[string]string{"X-Internal-Token": fenceInternalToken}

	// The crew does not exist in this fixture; what is under test is that the
	// route is reachable with the sidecar's credential at all. A 401 here is
	// the bug; a 404 from the handler would be a legitimate answer.
	rr := probeInternalFence(t, r, http.MethodGet,
		"/api/v1/internal/crews/crew-1/capabilities?workspace_id="+wsID, fenceLoopbackAddr, auth)
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("GET /internal/crews/{id}/capabilities: 401 — the sidecar credential was refused")
	}
	// The fence answers an unknown internal path with exactly this body, so
	// it is how "route not registered" is told apart from "handler said no".
	if rr.Code == http.StatusNotFound &&
		strings.Contains(rr.Body.String(), `"error":"Not Found"`) {
		t.Fatalf("route is not registered — the fence swallowed it as an unknown path")
	}
}

// The fence still fences: an internal route is not a public one.
func TestInternalPipelineReads_StillNeedTheToken(t *testing.T) {
	r, wsID := newFenceRouter(t)

	for _, target := range []string{
		"/api/v1/internal/pipelines?workspace_id=" + wsID,
		"/api/v1/internal/crews/crew-1/capabilities?workspace_id=" + wsID,
	} {
		rr := probeInternalFence(t, r, http.MethodGet, target, fenceAttackerAddr, nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s unauthenticated: status = %d, want the fence's 404 — body: %q",
				target, rr.Code, rr.Body.String())
		}
	}
}
