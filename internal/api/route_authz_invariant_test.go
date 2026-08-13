package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// route_authz_invariant_test.go — the build-time enumeration invariant that
// closes the "forgotten role gate" class (#809 / #811).
//
// #792 fixed the individually-exploited ungated mutations but guarded them
// with a HARD-CODED list (mutation_authz_test.go): it cannot catch the NEXT
// ungated route. These two tests make "every mutation route declares a role"
// a property of the route table itself, so a new mutation registered the old
// way fails the build.
//
// Scope: EVERY mutation registration in internal/api. The class the
// vulnerability lives in is the JWT-session surface — routes that used to
// register as `authed(wsCtx(...))` (workspace-scoped) or bare `authed(...)`
// (self-scoped), where a workspace member reaches a handler that forgot its
// inline check. The X-Internal-Token sidecar surface (`internalAuth(...)`) and
// the public token/HMAC dispatch routes (webhooks, waitpoint tokens,
// bootstrap/signup) are a different trust boundary — but since #1953 they are
// classified explicitly rather than passing because no rule happened to name
// them. An exemption by silence cannot tell "different fence" apart from "no
// fence", which is the only distinction that matters here.

// TestEveryMutationRouteDeclaresRole walks the recorded route table and asserts
// every mutation route carries a recognised role declaration. RED before the
// migration: no route flows through authedMut/authedSelfMut yet, so the table
// is empty and the "expected a populated table" guard fails.
func TestEveryMutationRouteDeclaresRole(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	if len(r.mutationRoutes) == 0 {
		t.Fatal("mutationRoutes is empty: no mutation route was registered through authedMut/authedSelfMut — the route-table chokepoint is not wired")
	}

	// A real instance has a large mutation surface; a tiny table means the
	// migration only covered a handful of routes.
	if len(r.mutationRoutes) < 150 {
		t.Errorf("only %d mutation routes recorded; expected the full surface (~200) — some router_*.go registrations were not migrated to authedMut/authedSelfMut", len(r.mutationRoutes))
	}

	seen := map[string]bool{}
	for _, mr := range r.mutationRoutes {
		key := mr.Method + " " + mr.Pattern
		if mr.Method == "" || mr.Pattern == "" {
			t.Errorf("route %q has empty method/pattern", key)
		}
		if !isDeclaredRole(mr.Role) {
			t.Errorf("mutation route %q declares role %q, which is not a recognised role — every mutation route must declare create/manage/self/inline", key, mr.Role)
		}
		if seen[key] {
			t.Errorf("mutation route %q registered twice", key)
		}
		seen[key] = true
	}
}

// ── Finding the registrars ────────────────────────────────────────────────
//
// #1953. Until that issue this test found its input with
// `filepath.Glob("router_*.go")`, which made a NAMING CONVENTION load-bearing
// for a security gate: a file that registered routes under any other name was
// invisible to the scan, and the only symptom was silence. `pages_internal.go`
// was exactly that file — it registers PUT /api/v1/internal/pages/{page}/data
// and nothing here ever looked at it.
//
// The set the gate checks is now defined by what registers routes, not by what
// somebody named a file. TestMutationRouteScanMissesNoRegistrarFile guards that
// property independently, so the NEXT such file fails the build rather than
// slipping past.

// routeRegistrationCall matches a route registration on the router, in any of
// the five shapes in use. It is deliberately looser than the pattern-matching
// regexes below — no `r.` receiver anchor, no literal method/path — because its
// only job is to decide whether a FILE registers routes at all. A file that
// registers dynamically (rbac_routes.go builds `method+" "+pattern`) has no
// literal route to classify but is still a registrar, and it must be read
// rather than assumed harmless.
var routeRegistrationCall = regexp.MustCompile(`\.(?:mux\.Handle|mux\.HandleFunc|authedMut|authedSelfMut|authedAdmin)\(`)

// routeRegistrarFiles returns every non-test .go file in this package that
// contains a route registration. Discovery is by content, never by filename.
func routeRegistrarFiles(t *testing.T) []string {
	t.Helper()

	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no .go files found — the test is looking in the wrong directory")
	}

	var files []string
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if !routeRegistrationCall.MatchString(readPackageFile(t, f)) {
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no route-registering files found — routeRegistrationCall has stopped matching, which would make every invariant in this file pass vacuously")
	}
	return files
}

func readPackageFile(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// ── Scanning the mutation surface ─────────────────────────────────────────

// muxMutationLine matches a mutation-verb ServeMux registration (single line;
// multi-line registrations put the verb on the first line, which is what we
// key on).
var muxMutationLine = regexp.MustCompile(`r\.mux\.(?:Handle|HandleFunc)\("(POST|PUT|PATCH|DELETE) ([^"]*)"`)

// wrapperMutationLine matches the recording wrappers, where the verb is an
// ARGUMENT rather than part of the pattern string. Missing this shape is the
// mistake route_read_scope_invariant_test.go documents having made with
// authedAdmin: a whole surface in no bucket, and nothing failing.
var wrapperMutationLine = regexp.MustCompile(`r\.authed(?:Mut|SelfMut)\("(POST|PUT|PATCH|DELETE)",\s*"([^"]*)"`)

// mutationRegistrationStart matches the beginning of ANY route registration.
// It bounds the wrapper lookahead: whatever follows belongs to the next route,
// so it must not be read as this one's wrapper. Same reasoning — and the same
// measured bug — as readRouteWrapperLookahead in the read-side twin.
var mutationRegistrationStart = regexp.MustCompile(`^\s*r\.(?:mux\.Handle|mux\.HandleFunc|authedMut|authedSelfMut|authedAdmin)\(`)

// mutationWrapperLookahead caps how many lines after a registration are joined
// before looking for the wrapper. registerInternalPageRoutes puts the handler
// argument on the line below the pattern, so a one-line window would classify
// it as carrying no wrapper at all.
const mutationWrapperLookahead = 4

type mutationRegistration struct {
	file string
	line int
	verb string
	path string
	tail string
	// wrapped means it registered through authedMut/authedSelfMut, which record
	// the route in r.mutationRoutes and enforce the declared role at the
	// chokepoint. TestEveryMutationRouteDeclaresRole checks the recorded table;
	// this file's job is to prove nothing bypassed it.
	wrapped bool
}

func (m mutationRegistration) key() string { return m.verb + " " + m.path }

func scanMutationRoutes(t *testing.T) []mutationRegistration {
	t.Helper()

	var routes []mutationRegistration
	for _, f := range routeRegistrarFiles(t) {
		lines := strings.Split(readPackageFile(t, f), "\n")
		for i, line := range lines {
			if m := wrapperMutationLine.FindStringSubmatch(line); m != nil {
				routes = append(routes, mutationRegistration{
					file: f, line: i + 1, verb: m[1], path: m[2],
					tail: line, wrapped: true,
				})
				continue
			}
			m := muxMutationLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			end := i + mutationWrapperLookahead
			if end > len(lines) {
				end = len(lines)
			}
			for j := i + 1; j < end; j++ {
				if mutationRegistrationStart.MatchString(lines[j]) {
					end = j
					break
				}
			}
			routes = append(routes, mutationRegistration{
				file: f, line: i + 1, verb: m[1], path: m[2],
				tail: strings.Join(lines[i:end], " "),
			})
		}
	}
	return routes
}

// ── The classification ────────────────────────────────────────────────────

// internalPrefix is the sidecar surface. Everything under it is fenced by
// X-Internal-Token (internalAuth) instead of the session/workspace/role chain,
// because its callers are agent containers holding a shared secret, not humans
// holding a JWT. That is a DIFFERENT fence, not a weaker one — and stating it
// here is the point: before #1953 these routes passed this test only because
// they happened not to contain the substring `authed(`, which is an exemption
// by silence. Now the two directions are both asserted:
//
//   - an internalAuth-wrapped route must live under /api/v1/internal/, so the
//     sidecar fence cannot be quietly applied to a session-facing route in
//     place of a role gate;
//   - a route under /api/v1/internal/ must be internalAuth-wrapped, so a new
//     internal route cannot land on the prefix with no fence at all.
const internalPrefix = "/api/v1/internal/"

// mutationRoutesOutsideTheRoleGate are the mutation routes that legitimately do
// not flow through authedMut/authedSelfMut and are not on the internal fence.
// Every one of them authenticates on something other than a session: a
// credential presented in the request, or nothing at all because the route is
// what CREATES the session.
//
// Each entry needs a reason a reviewer can check against the handler — "it is
// already in the map" is not one. Adding a route here is a claim that the role
// chokepoint has nothing to gate on, not that the route is unimportant.
//
// These were surfaced by #1953. They were never checked before: the old test
// only asked whether a mutation line contained the legacy `authed(` chain, so
// a registration with NO wrapper at all — the strongest possible form of
// "forgot the role gate" — read as clean.
var mutationRoutesOutsideTheRoleGate = map[string]string{
	// Pre-auth. These run before a session exists; there is no caller role to
	// gate on because there is no caller yet.
	"POST /api/v1/bootstrap": "creates the first owner on an empty database; gated by the bootstrap token " +
		"in AuthHandler.Bootstrap, which is the single point of defence for the deploy race (router_auth.go:30)",
	"POST /api/v1/auth/signup": "self-registration, subject to r.allowSignup; there is no session to derive a role from",
	"POST /api/v1/auth/forgot": "starts password recovery and answers 200 either way so it cannot be used to " +
		"enumerate accounts; writes only a reset token for the address supplied",
	"POST /api/v1/auth/reset": "redeems a recovery token — the token IS the credential (router_auth.go:43)",
	"POST /api/auth/callback/credentials": "NextAuth credential login; this is the route that MINTS the session " +
		"the role gate would otherwise read",
	"POST /api/auth/token/refresh": "NextAuth refresh; authenticates on the presented refresh token",
	"POST /api/auth/signout":       "destroys the caller's own session; nothing to authorise beyond holding it",

	// Token-scoped: the credential is in the request, and it names exactly one
	// resource. A role gate would need a workspace membership these callers do
	// not have by construction.
	"POST /api/v1/auth/pair/redeem": "redeems a single-use CLI pairing code with a 10-minute TTL; the code is " +
		"the credential (router_auth.go:118)",
	"POST /api/v1/webhooks/{token}": "public pipeline dispatch: the high-entropy token in the path is the auth " +
		"surface, with signing_secret + HMAC layered on top (router_pipelines.go:184)",
	"POST /api/v1/waitpoint-tokens/{token}": "an external system completes one waitpoint via its callback URL " +
		"with no workspace JWT; same token-is-the-auth model as webhook dispatch",
	"POST /api/v1/webhooks/{crewId}/{agentId}/trigger": "inbound agent webhook; WebhookHandler validates the " +
		"per-agent secret itself, and the route is not even registered unless the loopback URL is configured (#538)",
	"POST /api/v1/page-webhooks/{token}": "inbound page-webhook push: the token names one page's webhook and is " +
		"looked up by hash; the issuer's CRUD surface next to it IS role-gated (router_pages_webhooks.go:99)",
	"POST /api/v1/public/pages/{token}/unlock": "public link password check (§7.3.3): the bcrypt password arrives " +
		"in the body precisely so it never rides in the URL, and the token names one page; there is no session " +
		"to scope from (router_pages_public.go:54)",
}

// TestEveryMutationRouteDeclaresItsAuthority is the source guard: every
// mutation registration must land in exactly one bucket —
//
//	authedMut / authedSelfMut — role declared at registration and recorded.
//	internalAuth(...)         — the X-Internal-Token sidecar fence.
//	mutationRoutesOutsideTheRoleGate — a stated reason there is no role to gate on.
//
// Anything else fails the build. In particular the legacy `authed(...)` chain
// (which covers both `authed(wsCtx(...))` and bare `authed(...)`) is never
// allowlistable: it authenticates without declaring a role, which is the exact
// shape of #809/#811.
//
// RED before the migration: ~200 `r.mux.Handle("POST ...", authed(wsCtx(...)))`
// lines still exist.
func TestEveryMutationRouteDeclaresItsAuthority(t *testing.T) {
	routes := scanMutationRoutes(t)

	var legacy, unclassified, misfenced []string
	for _, rt := range routes {
		onInternalFence := strings.Contains(rt.tail, "internalAuth(")
		onInternalPrefix := strings.HasPrefix(rt.path, internalPrefix)

		// The sidecar fence and the internal prefix must agree, both ways.
		if onInternalFence != onInternalPrefix {
			what := "is wrapped in internalAuth() but does not live under " + internalPrefix +
				" — the sidecar fence is not a substitute for a declared role on a session-facing route"
			if onInternalPrefix {
				what = "lives under " + internalPrefix + " but is not wrapped in internalAuth() — " +
					"it is on the sidecar prefix with no fence"
			}
			misfenced = append(misfenced, formatOffender(rt.file, rt.line, rt.key()+" "+what))
			continue
		}
		if onInternalFence {
			continue
		}
		if rt.wrapped {
			continue // role declared at registration, recorded in r.mutationRoutes
		}
		// A mutation registration must go through a recording wrapper. The
		// legacy chain leaves the literal `authed(` on the line.
		if strings.Contains(rt.tail, "authed(") {
			legacy = append(legacy, formatOffender(rt.file, rt.line, rt.key()))
			continue
		}
		if _, ok := mutationRoutesOutsideTheRoleGate[rt.key()]; ok {
			continue
		}
		unclassified = append(unclassified, formatOffender(rt.file, rt.line, rt.key()))
	}

	if len(legacy) > 0 {
		t.Errorf("%d mutation route(s) still registered via the legacy authed(...) chain instead of authedMut/authedSelfMut — every mutation route must declare a role at registration:\n%s",
			len(legacy), strings.Join(legacy, "\n"))
	}
	if len(misfenced) > 0 {
		t.Errorf("%d mutation route(s) disagree with the internal trust boundary:\n%s",
			len(misfenced), strings.Join(misfenced, "\n"))
	}
	if len(unclassified) > 0 {
		t.Errorf(`%d mutation route(s) carry no authority declaration at all:
%s

Fix one of these ways:
  - register it with r.authedMut(method, pattern, role, h) or r.authedSelfMut(...)
    so the role comes from the chokepoint (preferred), or
  - if it is a sidecar route, put it under %s behind internalAuth(...), or
  - add it to mutationRoutesOutsideTheRoleGate in this file WITH a reason,
    verified against the handler, explaining what it authenticates on instead.
Do not add a bare name to the map to make this test pass — the map is a review record.`,
			len(unclassified), strings.Join(unclassified, "\n"), internalPrefix)
	}
}

// TestMutationRouteScanMissesNoRegistrarFile is the #1953 guard, and it is the
// more important test in this file: it proves the gate above is looking at the
// whole surface rather than at whatever happens to be named router_*.go.
//
// It re-finds the registrar files with a detector that shares no code with
// routeRegistrarFiles — a plain literal-registration match over every non-test
// file in the package — and fails when the gate's scan did not reach one. RED
// before #1953: pages_internal.go registers PUT /api/v1/internal/pages/{page}/data
// and the router_*.go glob could not see it.
func TestMutationRouteScanMissesNoRegistrarFile(t *testing.T) {
	literalMutation := regexp.MustCompile(`\.(?:mux\.Handle|mux\.HandleFunc)\("(?:POST|PUT|PATCH|DELETE) |\.authed(?:Mut|SelfMut)\("(?:POST|PUT|PATCH|DELETE)"`)

	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	scanned := map[string]bool{}
	for _, rt := range scanMutationRoutes(t) {
		scanned[rt.file] = true
	}

	var missed []string
	for _, f := range all {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if !literalMutation.MatchString(readPackageFile(t, f)) {
			continue
		}
		if !scanned[f] {
			missed = append(missed, "  "+f)
		}
	}
	if len(missed) > 0 {
		t.Fatalf(`%d file(s) register mutation routes that the authz gate never scanned:
%s

Every mutation route in internal/api must be visible to
TestEveryMutationRouteDeclaresItsAuthority. Discovery is by content
(routeRegistrationCall), not by filename — if a file is missing here, the
registration shape it uses is one the scan does not recognise. Widen the scan;
do not rename the file.`, len(missed), strings.Join(missed, "\n"))
	}
}

// TestMutationRouteScanFindsTheRealSurface guards the guard the other way. If a
// registration regex stops matching, TestEveryMutationRouteDeclaresItsAuthority
// passes vacuously — it finds nothing to complain about and reports success.
func TestMutationRouteScanFindsTheRealSurface(t *testing.T) {
	routes := scanMutationRoutes(t)

	// 370 mutation registrations on the tree this landed against: 314 through
	// the recording wrappers and 56 raw r.mux.Handle. Both forms are counted
	// separately, because a single total would hide one regex rotting while the
	// other still matched.
	const minRoutes = 300
	if len(routes) < minRoutes {
		t.Fatalf("scanned only %d mutation routes, expected at least %d — a registration regex has likely stopped matching, which would make TestEveryMutationRouteDeclaresItsAuthority pass vacuously",
			len(routes), minRoutes)
	}

	wrapped, raw := 0, 0
	for _, rt := range routes {
		if rt.wrapped {
			wrapped++
		} else {
			raw++
		}
	}
	if wrapped < 250 {
		t.Errorf("scanned only %d authedMut/authedSelfMut registrations, expected at least 250 — wrapperMutationLine has likely stopped matching, which drops the entire role-declared surface from this invariant", wrapped)
	}
	if raw < 40 {
		t.Errorf("scanned only %d r.mux.Handle mutation registrations, expected at least 40 — muxMutationLine has likely stopped matching, which drops exactly the un-wrapped routes this gate exists to catch", raw)
	}

	// Every allowlist entry must still name a real route. A stale entry
	// excuses nothing today and silently excuses whatever reuses the pattern
	// tomorrow.
	live := make(map[string]bool, len(routes))
	for _, rt := range routes {
		live[rt.key()] = true
	}
	for key := range mutationRoutesOutsideTheRoleGate {
		if !live[key] {
			t.Errorf("mutationRoutesOutsideTheRoleGate has a stale entry %q — no such mutation route is registered; remove it", key)
		}
	}

	// A reason is the whole point of the map. An empty one is not a reason.
	for key, reason := range mutationRoutesOutsideTheRoleGate {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("mutationRoutesOutsideTheRoleGate[%q] has no reason", key)
		}
	}
}

func formatOffender(file string, line int, text string) string {
	return "  " + file + ":" + strconv.Itoa(line) + ": " + strings.TrimSpace(text)
}

// TestRequireRoleMW_Enforcement proves the declared-role middleware is the
// enforcement point: an under-privileged role gets 403 before the handler
// runs; the intended tier reaches the handler; and the handler-authoritative
// sentinels pass through unconditionally (their finer gate lives in the
// handler). This is the acceptance guarantee — a VIEWER hitting a declared
// mutation route is refused by the chokepoint, not by a hand-placed inline
// check that a future handler might forget.
func TestRequireRoleMW_Enforcement(t *testing.T) {
	r := &Router{}
	reached := "REACHED"
	h := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(reached)) }

	cases := []struct {
		role       string
		callerRole string
		wantReach  bool
	}{
		// roleCreate = MANAGER+ (create/update tier)
		{roleCreate, "VIEWER", false},
		{roleCreate, "MEMBER", false},
		{roleCreate, "MANAGER", true},
		{roleCreate, "ADMIN", true},
		{roleCreate, "OWNER", true},
		// roleManage = ADMIN+ (manage/delete tier)
		{roleManage, "MANAGER", false},
		{roleManage, "ADMIN", true},
		{roleManage, "OWNER", true},
		// sentinels: handler-authoritative, middleware never blocks
		{roleSelf, "VIEWER", true},
		{roleInline, "VIEWER", true},
		{roleInline, "MEMBER", true},
	}
	for _, c := range cases {
		t.Run(c.role+"/"+c.callerRole, func(t *testing.T) {
			// scopeSelf isolates the role gate under test from the scope gate.
			mw := r.requireRoleScopeMW(c.role, scopeSelf, h)
			rr := httptest.NewRecorder()
			req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), "u1", "w1", c.callerRole)
			mw.ServeHTTP(rr, req)
			reachedHandler := rr.Body.String() == reached
			if reachedHandler != c.wantReach {
				t.Fatalf("role=%s caller=%s: reachedHandler=%v (code=%d), want reach=%v",
					c.role, c.callerRole, reachedHandler, rr.Code, c.wantReach)
			}
			if !c.wantReach && rr.Code != http.StatusForbidden {
				t.Fatalf("role=%s caller=%s: code=%d, want 403", c.role, c.callerRole, rr.Code)
			}
		})
	}
}
