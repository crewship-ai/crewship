package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// #1501 — the internal surface must not be enumerable.
//
// requireInternal answers 404 (not 403) for an unauthorised caller precisely so
// /api/v1/internal/* is indistinguishable from routes that do not exist. But
// http.ServeMux matches the PATTERN and rejects the METHOD before any
// middleware runs, so `GET /api/v1/internal/keeper/request` (a POST-only route)
// used to return 405 straight from the mux with the fence never consulted —
// a positive existence signal for an unauthenticated prober.
//
// These tests drive the whole Router (ServeHTTP → EnforceOrigin →
// routeWithRateLimiting → mux) so they exercise the real dispatch path, and
// assert the STRONG property: every rejected probe under the prefix produces a
// byte-identical response — same status, same body, same Content-Type, and no
// Allow header — whatever the reason for the rejection.
// ---------------------------------------------------------------------------

const (
	// fenceInternalToken is the router's master internal token in these tests.
	// Never sent by the attacker probes; used only by the legitimate-caller test.
	fenceInternalToken = "internal-fence-test-token-abcdef"
	// fenceAttackerAddr is TEST-NET-3 (203.0.113.0/24) — a PUBLIC address, so
	// requireInternal's network gate treats it as an outside caller.
	fenceAttackerAddr = "203.0.113.7:54321"
	// fenceLoopbackAddr is what a legitimate host-side caller (chatbridge
	// resolver, llmproxy monitor) and the on-box harness look like.
	fenceLoopbackAddr = "127.0.0.1:41234"
)

// newFenceRouter builds a fully-registered Router with a known internal token
// and a seeded workspace, with the internal-API kill-switches pinned off so the
// assertions do not depend on the developer's environment.
func newFenceRouter(t *testing.T) (*Router, string) {
	t.Helper()
	t.Setenv("CREWSHIP_INTERNAL_ALLOW_ANY", "false")
	t.Setenv("CREWSHIP_INTERNAL_TRUSTED_PROXIES", "")

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithInternalToken(fenceInternalToken))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r, wsID
}

// fenceProbe sends one request through the full router stack.
func fenceProbe(t *testing.T, r *Router, method, target, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut {
		body = strings.NewReader(`{}`)
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// assertIndistinguishable404 is the whole point of #1501: a rejected probe must
// carry no signal at all — not in the status, not in the body, and not in an
// Allow header the mux would otherwise volunteer.
func assertIndistinguishable404(t *testing.T, name string, rr *httptest.ResponseRecorder, wantBody string) {
	t.Helper()
	if rr.Code != http.StatusNotFound {
		t.Errorf("%s: status = %d, want 404 (405 leaks that the route exists) — body: %q",
			name, rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Allow"); got != "" {
		t.Errorf("%s: Allow header = %q, want absent — it enumerates the route's real methods", name, got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("%s: Content-Type = %q, want application/json (must match the fence 404)", name, got)
	}
	if got := rr.Body.String(); got != wantBody {
		t.Errorf("%s: body = %q, want %q (must be byte-identical to the fence 404)", name, got, wantBody)
	}
}

// TestInternalSurface_MethodMismatchIs404_NotEnumerable covers the six failing
// assertions from scripts/test-harness/test-attack-surface.sh (B1–B6) plus the
// method-mismatch and unknown-path shapes that produce the same leak.
func TestInternalSurface_MethodMismatchIs404_NotEnumerable(t *testing.T) {
	r, wsID := newFenceRouter(t)

	// The reference response: an unauthorised caller hitting a route that
	// EXISTS with the RIGHT method. This is the fence 404 every other probe
	// has to be indistinguishable from.
	ref := fenceProbe(t, r, http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, fenceAttackerAddr, nil)
	if ref.Code != http.StatusNotFound {
		t.Fatalf("reference fence probe: status = %d, want 404 — body: %q", ref.Code, ref.Body.String())
	}
	wantBody := ref.Body.String()
	if wantBody == "" {
		t.Fatal("reference fence probe produced an empty body; the comparison below would be vacuous")
	}

	cases := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		// ── the harness's Tier A B1–B6 ──────────────────────────────────
		{"B1 /internal/credentials no token", http.MethodGet,
			"/api/v1/internal/credentials?workspace_id=" + wsID, nil},
		{"B2 /internal/credentials via user JWT", http.MethodGet,
			"/api/v1/internal/credentials?workspace_id=" + wsID,
			map[string]string{"Authorization": "Bearer not-a-real-user-jwt"}},
		{"B3 /internal/keeper/request no token", http.MethodPost,
			"/api/v1/internal/keeper/request",
			map[string]string{"Content-Type": "application/json"}},
		{"B4 /internal/keeper/request guessed static token", http.MethodPost,
			"/api/v1/internal/keeper/request",
			map[string]string{"Content-Type": "application/json", "X-Internal-Token": "internal-dev-token"}},
		{"B5 /internal/agents no token", http.MethodGet,
			"/api/v1/internal/agents?workspace_id=" + wsID, nil},
		{"B6 spoofed X-Forwarded-For does not fake a private origin", http.MethodGet,
			"/api/v1/internal/agents?workspace_id=" + wsID,
			map[string]string{"X-Forwarded-For": "127.0.0.1"}},

		// ── method mismatch on routes that really exist ─────────────────
		{"GET on the POST-only keeper request route", http.MethodGet,
			"/api/v1/internal/keeper/request", nil},
		{"DELETE on the credentials route", http.MethodDelete,
			"/api/v1/internal/credentials?workspace_id=" + wsID, nil},
		{"GET on the POST-only pipelines/save route", http.MethodGet,
			"/api/v1/internal/pipelines/save", nil},
		{"PATCH on the GET+POST crews route", http.MethodPatch,
			"/api/v1/internal/crews?workspace_id=" + wsID, nil},
		{"GET on the POST-only assignments route", http.MethodGet,
			"/api/v1/internal/assignments", nil},
		{"PUT on the POST-only journal emit route", http.MethodPut,
			"/api/v1/internal/journal/emit", nil},

		// ── paths that do not exist at all: same answer, or the pair
		//    "exists but wrong method" vs "does not exist" is the oracle ──
		{"unknown internal path", http.MethodGet,
			"/api/v1/internal/definitely-not-a-real-route", nil},
		{"unknown internal path, POST", http.MethodPost,
			"/api/v1/internal/definitely-not-a-real-route", nil},
		{"unknown internal subtree", http.MethodGet,
			"/api/v1/internal/keeper/definitely-not-a-real-route", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := fenceProbe(t, r, tc.method, tc.target, fenceAttackerAddr, tc.headers)
			assertIndistinguishable404(t, tc.name, rr, wantBody)
		})
	}
}

// TestInternalSurface_MethodMismatchIs404_FromPrivateOrigin — the same
// property must hold for a caller the network gate lets through (RFC1918 / the
// on-box harness). Otherwise 405-vs-403 tells a LAN-position prober exactly
// which paths are real, which is the same map the edge probe is denied.
func TestInternalSurface_MethodMismatchIs404_FromPrivateOrigin(t *testing.T) {
	r, wsID := newFenceRouter(t)

	// Reference: an unauthorised LOOPBACK caller on a real route+method is
	// rejected by the token compare with 403 Forbidden — deliberately, since
	// the network gate already vouched for the origin. What must NOT happen
	// is a 405, which distinguishes a real path from a fabricated one before
	// any credential is checked.
	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"GET on the POST-only keeper request route", http.MethodGet, "/api/v1/internal/keeper/request"},
		{"DELETE on the credentials route", http.MethodDelete, "/api/v1/internal/credentials?workspace_id=" + wsID},
		{"unknown internal path", http.MethodGet, "/api/v1/internal/definitely-not-a-real-route"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := fenceProbe(t, r, tc.method, tc.target, fenceLoopbackAddr,
				map[string]string{"X-Internal-Token": fenceInternalToken})
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404 — body: %q", tc.name, rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != "" {
				t.Errorf("%s: Allow header = %q, want absent", tc.name, got)
			}
		})
	}
}

// TestInternalSurface_LegitimateCallStillWorks — the fence must reject only
// what the mux would have rejected anyway. A correctly addressed, correctly
// authenticated sidecar/host-side call still reaches its handler, and HEAD
// still rides the GET pattern the way net/http defines it.
func TestInternalSurface_LegitimateCallStillWorks(t *testing.T) {
	r, wsID := newFenceRouter(t)

	auth := map[string]string{"X-Internal-Token": fenceInternalToken}

	rr := fenceProbe(t, r, http.MethodGet, "/api/v1/internal/crews?workspace_id="+wsID, fenceLoopbackAddr, auth)
	if rr.Code != http.StatusOK {
		t.Fatalf("legitimate GET /internal/crews: status = %d, want 200 — body: %q", rr.Code, rr.Body.String())
	}
	if body := strings.TrimSpace(rr.Body.String()); body != "[]" {
		t.Errorf("legitimate GET /internal/crews: body = %q, want []", body)
	}

	// HEAD is registered implicitly alongside every GET pattern by
	// net/http. If the existence check ever stops delegating to the mux's
	// own matcher, this is the first thing that breaks.
	rr = fenceProbe(t, r, http.MethodHead, "/api/v1/internal/crews?workspace_id="+wsID, fenceLoopbackAddr, auth)
	if rr.Code != http.StatusOK {
		t.Errorf("legitimate HEAD /internal/crews: status = %d, want 200", rr.Code)
	}

	// A wildcard route still dispatches AND still resolves its path value.
	// The existence check asks the mux the same question ServeHTTP does, and
	// ServeHTTP re-derives the match, so {agentId} must still bind — this
	// handler's own "Agent not found" (rather than the fence's "Not Found")
	// is the proof that it did.
	rr = fenceProbe(t, r, http.MethodGet, "/api/v1/internal/agents/no-such-agent/resolve?workspace_id="+wsID, fenceLoopbackAddr, auth)
	if !strings.Contains(rr.Body.String(), "Agent not found") {
		t.Errorf("wildcard route did not reach its handler with {agentId} bound — status %d, body: %q",
			rr.Code, rr.Body.String())
	}
}

// TestInternalSurface_NonCanonicalPathGetsTheFence404 closes the second door
// onto the surface.
//
// routeWithRateLimiting decides whether a request belongs to the internal
// prefix, and it used to decide on the RAW path. ServeMux, however, cleans a
// path before matching, so `//api/v1/internal/credentials`,
// `/api/v1//internal/credentials` and `/api/v1/./internal/credentials` all
// address the same route while none of them literally starts with
// "/api/v1/internal/". Those spellings therefore skipped serveInternal entirely
// and landed in the mux, which answers a non-canonical path with
// `307 Temporary Redirect` and a `Location` holding the CLEANED path — the mux
// handing back the canonical internal route to a caller the fence had just
// finished refusing to confirm anything to.
//
// The response must be the ordinary fence 404, byte for byte, with no Location.
func TestInternalSurface_NonCanonicalPathGetsTheFence404(t *testing.T) {
	r, wsID := newFenceRouter(t)

	ref := fenceProbe(t, r, http.MethodGet, "/api/v1/internal/credentials?workspace_id="+wsID, fenceAttackerAddr, nil)
	if ref.Code != http.StatusNotFound {
		t.Fatalf("reference fence probe: status = %d, want 404", ref.Code)
	}
	wantBody := ref.Body.String()

	for _, tc := range []struct {
		name   string
		method string
		target string
	}{
		{"leading double slash, real route", http.MethodGet, "//api/v1/internal/credentials?workspace_id=" + wsID},
		{"leading double slash, fake route", http.MethodGet, "//api/v1/internal/definitely-not-a-real-route"},
		{"inner double slash, real route", http.MethodGet, "/api/v1//internal/credentials?workspace_id=" + wsID},
		{"dot segment, real route", http.MethodGet, "/api/v1/./internal/credentials?workspace_id=" + wsID},
		{"dot-dot rejoining the prefix", http.MethodGet, "/api/v1/internal/keeper/../credentials?workspace_id=" + wsID},
		{"non-canonical spelling of a POST-only route", http.MethodGet, "//api/v1/internal/keeper/request"},
		{"trailing double slash", http.MethodGet, "/api/v1/internal/credentials//?workspace_id=" + wsID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := fenceProbe(t, r, tc.method, tc.target, fenceAttackerAddr, nil)
			assertIndistinguishable404(t, tc.name, rr, wantBody)
			if loc := rr.Header().Get("Location"); loc != "" {
				t.Errorf("%s: Location = %q, want absent — a redirect echoes back the canonical internal path",
					tc.name, loc)
			}
		})
	}

	// The same must hold for a caller the network gate lets through, so a
	// non-canonical spelling is not a way around the fence from the LAN either.
	rr := fenceProbe(t, r, http.MethodGet, "//api/v1/internal/keeper/request", fenceLoopbackAddr,
		map[string]string{"X-Internal-Token": fenceInternalToken})
	if rr.Code != http.StatusNotFound {
		t.Errorf("private-origin non-canonical probe: status = %d, want 404 — body: %q", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "" {
		t.Errorf("private-origin non-canonical probe: Location = %q, want absent", loc)
	}
}

// internalSubtreePattern matches an /api/v1/internal/... route pattern
// registered as a SUBTREE — i.e. ending in "/" before the closing quote.
var internalSubtreePattern = regexp.MustCompile(`"[A-Z]+ ` + regexp.QuoteMeta(internalPathPrefix) + `[^"]*/"`)

// TestInternalRoutes_NoSubtreePatterns is the guard serveInternal's doc comment
// points at.
//
// internalRouteDispatches asks the mux whether it would dispatch, and treats a
// non-empty pattern as yes. That is right for every ServeMux outcome except
// one: when a SUBTREE pattern (`GET /api/v1/internal/foo/`) is registered and a
// request omits the trailing slash, net/http answers `307` to `/foo/` while
// still reporting `/foo/` as the pattern. The fence would wave that through and
// the redirect would confirm the route exists — exactly the signal #1501 closed.
//
// The case cannot be detected from the returned pattern (a subtree pattern also
// legitimately serves paths with no trailing slash), so it is prevented at the
// registration end instead: no internal route may be a subtree. Every internal
// route today is an exact path with an explicit method, and a wildcard segment
// ({crewId}, {agentId}) is not a subtree — those are unaffected.
func TestInternalRoutes_NoSubtreePatterns(t *testing.T) {
	const routerFile = "router_internal.go"
	src, err := os.ReadFile(routerFile)
	if err != nil {
		t.Fatalf("read %s: %v", routerFile, err)
	}
	if !strings.Contains(string(src), internalPathPrefix) {
		t.Fatalf("%s contains no %q registrations — this test is looking in the wrong place",
			routerFile, internalPathPrefix)
	}

	var offenders []string
	for i, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if internalSubtreePattern.MatchString(line) {
			offenders = append(offenders, "  "+routerFile+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d internal route(s) registered as a subtree pattern (trailing \"/\").\n"+
			"net/http answers the no-trailing-slash form with a 307 to the cleaned path, and reports the\n"+
			"subtree pattern while doing it — so serveInternal's fence lets it through and the redirect\n"+
			"confirms the route exists (#1501). Register the exact path instead, or teach\n"+
			"internalRouteDispatches to detect the redirect:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
