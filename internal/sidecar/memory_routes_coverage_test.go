package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file is the structural half of the CRE-153 fix.
//
// The vulnerability #1274 tried to close was re-opened by the SHAPE of the
// router: buildHandler is a flat hand-written switch in which each handler
// opts into the identity check itself. #1274 added the check to one arm and
// five sibling arms ten lines away kept serving token-less callers. A test
// that only exercises the routes someone remembered to think about cannot
// catch that class, so this test derives the route list from the SOURCE of
// buildHandler and forces every route to be classified.
//
// Adding a route to buildHandler without adding a line to sidecarRouteGuards
// fails this test. That is the property that makes "chokepoint" true rather
// than aspirational.

// routeGuardKind is how a registered route is protected.
type routeGuardKind int

const (
	// guardMemoryChokepoint — covered by refuseUnauthorizedMemory in
	// buildHandler, ahead of the route switch. Exercised end-to-end below:
	// a token-less request on a tokens-provisioned crew must get 403.
	guardMemoryChokepoint routeGuardKind = iota
	// guardHandlerIdentity — the handler resolves the acting agent itself
	// (actingAgentID / actingIdentity / tokenlessDowngrade) and fails closed.
	// These are NOT behind a prefix gate; the assertion below is structural
	// (the handler must reference one of those helpers), which is the honest
	// statement of what the code guarantees.
	guardHandlerIdentity
	// guardNone — the route carries no per-agent identity decision: it is
	// either read-only crew-scoped data, a pure forward to crewshipd (which
	// applies its own authorization), or an unauthenticated status surface.
	// Reaching it still requires the loopback + Host gate, i.e. code running
	// inside the crew container.
	guardNone
)

// sidecarRouteGuards classifies every route registered in buildHandler.
// The keys are derived mechanically from the source (see routeKeysFromSource),
// so this table cannot drift from the router without the test failing.
var sidecarRouteGuards = map[string]routeGuardKind{
	// --- memory surface: guarded by the prefix chokepoint --------------
	"POST /memory/search":  guardMemoryChokepoint,
	"POST /memory/write":   guardMemoryChokepoint,
	"GET /memory/read":     guardMemoryChokepoint,
	"GET /memory/status":   guardMemoryChokepoint,
	"POST /memory/reindex": guardMemoryChokepoint,
	"POST /mcp/memory":     guardMemoryChokepoint,
	"POST /mcp/memory/":    guardMemoryChokepoint,

	// --- routes that resolve the acting agent inside the handler -------
	"POST /query":        guardHandlerIdentity,
	"POST /escalate":     guardHandlerIdentity,
	"POST /issue/create": guardHandlerIdentity,
	// The rest of the issue surface (issue_verbs.go). READS are on this list
	// deliberately: the board is crew data, and a sibling that omits its
	// header must not fall back to the boot agent's identity to read it.
	"GET /issues":           guardHandlerIdentity,
	"GET /issue/":           guardHandlerIdentity,
	"PATCH /issue/":         guardHandlerIdentity,
	"POST /issue/ /comment": guardHandlerIdentity,
	"POST /issue/ /link":    guardHandlerIdentity,
	// Attachments (#1768 item 7). The READ arms are guardHandlerIdentity for
	// the same reason the rest of the issue reads are, and one more besides:
	// these return FILE CONTENT, so a token-less sibling falling back to the
	// boot agent's identity would be reading files, not just metadata.
	"GET /issue/ /attachments":  guardHandlerIdentity,
	"GET /issue/ /attachments/": guardHandlerIdentity,
	"POST /issue/ /attachments": guardHandlerIdentity,
	"POST /expose-port":         guardHandlerIdentity,
	"POST /keeper/request":      guardHandlerIdentity,
	"POST /keeper/execute":      guardHandlerIdentity,
	"POST /mcp/routines":        guardHandlerIdentity,
	// notify_send is authorised by the agent↔channel pairing on the server,
	// which keys on the ACTING agent — so this route must resolve identity or
	// the grant model means nothing.
	"POST /mcp/notify":            guardHandlerIdentity,
	"POST /pipelines/save":        guardHandlerIdentity,
	"POST /pipelines/ /run":       guardHandlerIdentity,
	"POST /connections/ /message": guardHandlerIdentity,
	"POST /report-confidence":     guardHandlerIdentity,
	// Both of these sat at guardNone while their handlers resolved identity.
	// /assign grew the check in #1757 (assignment.go:59) and /mission/create
	// has carried it since #812 (mission.go:125) — neither entry was updated,
	// and the one-directional assertion could not see it. The converse in
	// TestSidecarRoutes_IdentityCoverage is what caught them; keep them here
	// rather than "fixing" the test, since the CODE is right in both cases and
	// it was the table that lied.
	"POST /assign":         guardHandlerIdentity,
	"POST /mission/create": guardHandlerIdentity,

	// The page producer door (#1946). Per-agent, not crew-scoped: the panel
	// names ONE producer (docs/prd/pages.md §7.1 rule 4) and the server checks
	// the acting agent against it, so a sibling that pushed as the boot agent
	// would be writing a panel it does not own.
	"PUT /pages/": guardHandlerIdentity,

	// --- no per-agent identity decision --------------------------------
	// Crew-scoped routes: they act on a resource owned by the CREW, forward
	// to crewshipd with the crew's IPC credentials, and record no per-agent
	// attribution. Any member of the crew may drive them — that is the
	// existing (unchanged) trust boundary, not something this table papers
	// over. Note it explicitly so a future reader does not read guardNone as
	// "checked and safe for per-agent isolation".
	"POST /mission/ /start":       guardNone,
	"POST /pipelines/ /dry_run":   guardNone,
	"GET /connections/ /messages": guardNone,
	"GET /connections/ /files":    guardNone,
	"POST /connections/ /files":   guardNone,

	"GET /results/":                   guardNone,
	"GET /standup":                    guardNone,
	"GET /mission/templates":          guardNone,
	"GET /mission/":                   guardNone,
	"GET /crews":                      guardNone,
	"POST /crew/create":               guardNone,
	"POST /agent/create":              guardNone,
	"POST /spawn":                     guardNone,
	"GET /credentials":                guardNone,
	"GET /crew-connections":           guardNone,
	"GET /manifest":                   guardNone,
	"PATCH /manifest":                 guardNone,
	"GET /pipelines":                  guardNone,
	"GET /pipelines/":                 guardNone,
	"POST /routines/schedules/create": guardNone,
	"POST /skills/generate":           guardNone,
	"POST /skills/author":             guardNone,
	"POST /credentials/create":        guardNone,
	"POST /credentials/ /rotate":      guardNone,
	"GET /mcp/tools":                  guardNone,
	"POST /mcp/call":                  guardNone,
	"GET /mcp/status":                 guardNone,
	"GET /connections":                guardNone,
}

// A NOTE ON THE OTHER QUESTION, and where it is answered.
//
// The table above asks "whose identity is this?". It never asked "may they?",
// and #1768 exists because the second question had no enforcement anywhere
// while every layer believed another layer was asking it. This block records
// how that was resolved, because the belief is the interesting part and a
// reader who only sees today's working code will not know it was ever there.
//
// The belief was written down on both sides, which is the only reason it
// survived. internal_routines.go said the autonomy gate "is enforced
// upstream of this handler in the /spawn-style entry; this surface receives
// only post-autonomy calls from the sidecar". routine_schedule.go — the
// sidecar handler that is that caller — said "the sidecar does not inspect
// role, capability, or caller identity beyond passing the headers through
// … keeping enforcement in one place avoids drift". Each file named the other
// as the enforcement point. Neither enforced. capabilities_check.go made the
// same move a third time for its userID=="" branch. All three claims have
// been rewritten to describe enforcement that now exists.
//
// The gate could not live here. The sidecar has no transport for
// autonomy_level: it is absent from IPCConfig and from every
// /api/v1/internal route, and the only exposure is the JWT-authed public
// /api/v1/crews/{id}/policy, which a sidecar cannot reach. A boot-time copy
// would be worse than none — it could never be invalidated when an operator
// lowers the level, which is exactly what policy.Resolver.Invalidate exists
// to do. So the gate went to the backend adapters, where it also covers every
// other holder of the internal token rather than just this one.
//
// All six routes are now gated on the crew's autonomy_level, in
// internal/api/internal_autonomy_gate.go and its callers:
//
//	POST /crew/create               → policy.ActionCrewCreate
//	POST /agent/create              → policy.ActionAgentCreate
//	POST /mission/create            → policy.ActionMissionCreate
//	POST /routines/schedules/create → policy.ActionRoutineScheduleCreate
//	POST /skills/generate           → policy.ActionSkillCreate
//	POST /skills/author             → policy.ActionSkillCreate
//
// The enumeration that used to sit here is gone deliberately rather than
// inverted into a "these are gated" list: such a list could only assert that
// the routes still exist, which is not the property worth defending. The
// property worth defending is that each handler still refuses — and that is
// pinned behaviourally, one test per route and per decision arm, in
// internal/api/internal_autonomy_gate_test.go. A source-shaped table here
// would go green against a handler whose gate had been gutted.

var (
	caseLineRe  = regexp.MustCompile(`^\s*case\s+(.*)$`)
	methodRe    = regexp.MustCompile(`http\.Method([A-Za-z]+)`)
	strLitRe    = regexp.MustCompile(`"([^"]*)"`)
	handlerCall = regexp.MustCompile(`s\.(handle[A-Za-z0-9_]*)\(`)
)

// sidecarRoute is one arm of the buildHandler switch, recovered from source.
type sidecarRoute struct {
	key     string // "<METHOD> <path literals…>"
	handler string // handleFoo
}

// routeKeysFromSource parses the buildHandler switch in server.go and returns
// one entry per registered route. Deriving the list from source (rather than
// hand-maintaining it) is the point: a route added to the switch shows up here
// whether or not anyone remembered this test exists.
func routeKeysFromSource(t *testing.T) []sidecarRoute {
	t.Helper()
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "func (s *Server) buildHandler(") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "proxy.ServeHTTP(w, r)") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("could not locate the buildHandler switch in server.go — update this test")
	}

	var routes []sidecarRoute
	for i := start; i < end; i++ {
		m := caseLineRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		cond := m[1]
		mm := methodRe.FindStringSubmatch(cond)
		if mm == nil {
			continue // not a method+path arm (e.g. a plain default)
		}
		method := strings.ToUpper(mm[1])
		seen := map[string]bool{}
		var parts []string
		for _, lit := range strLitRe.FindAllStringSubmatch(cond, -1) {
			p := lit[1]
			if !strings.HasPrefix(p, "/") || len(p) < 2 || seen[p] {
				continue
			}
			seen[p] = true
			parts = append(parts, p)
		}
		if len(parts) == 0 {
			continue
		}
		// Find the handler invoked by this arm. Several arms carry a long
		// explanatory comment between the case and the call, so scan until
		// the next case rather than a fixed window.
		handler := ""
		for j := i; j < end; j++ {
			if j > i && caseLineRe.MatchString(lines[j]) {
				break
			}
			if hm := handlerCall.FindStringSubmatch(lines[j]); hm != nil {
				handler = hm[1]
				break
			}
		}
		routes = append(routes, sidecarRoute{
			key:     method + " " + strings.Join(parts, " "),
			handler: handler,
		})
	}
	if len(routes) < 20 {
		t.Fatalf("recovered only %d routes from buildHandler — the parser is broken", len(routes))
	}
	return routes
}

// packageHandlerBodies returns methodName → source body for every
// `func (s *Server) X(...)` in the package (non-test files).
func packageHandlerBodies(t *testing.T) map[string]string {
	t.Helper()
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{}
	funcStart := regexp.MustCompile(`^func \(s \*Server\) ([A-Za-z0-9_]+)\(`)
	for _, f := range entries {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		cur := ""
		var buf []string
		flush := func() {
			if cur != "" {
				bodies[cur] = strings.Join(buf, "\n")
			}
			cur, buf = "", nil
		}
		for _, l := range lines {
			if m := funcStart.FindStringSubmatch(l); m != nil {
				flush()
				cur = m[1]
				continue
			}
			if strings.HasPrefix(l, "func ") {
				flush()
				continue
			}
			if cur != "" {
				buf = append(buf, l)
			}
		}
		flush()
	}
	return bodies
}

// identityHelpers are the calls that resolve (and fail closed on) the acting
// agent behind a sidecar request.
var identityHelpers = []string{
	"actingAgentID(r)",
	"actingIdentity(r)",
	"tokenlessDowngrade(r)",
	"refuseUnauthorizedMemory(w, r)",
}

// serverMethodCall matches a call to another method on the same Server, so the
// identity check can follow one or two hops — several MCP handlers parse the
// envelope first and resolve identity in the tools/call responder they
// delegate to.
var serverMethodCall = regexp.MustCompile(`s\.([A-Za-z0-9_]+)\(`)

// identityWalkDepth is how many delegation hops the identity search follows.
// Both directions of the classification assertion use it, and they must: a
// route proved "identity-resolving" at one depth and "not" at another would
// let a handler be simultaneously mis-classifiable both ways.
const identityWalkDepth = 2

func resolvesActingIdentity(name string, bodies map[string]string, depth int) bool {
	return handlerCalls(name, bodies, depth, identityHelpers)
}

// handlerCalls reports whether the handler named name — or a Server method it
// delegates to within depth hops — contains any of needles.
func handlerCalls(name string, bodies map[string]string, depth int, needles []string) bool {
	body, ok := bodies[name]
	if !ok {
		return false
	}
	for _, h := range needles {
		if strings.Contains(body, h) {
			return true
		}
	}
	if depth <= 0 {
		return false
	}
	for _, m := range serverMethodCall.FindAllStringSubmatch(body, -1) {
		if m[1] == name {
			continue
		}
		if handlerCalls(m[1], bodies, depth-1, needles) {
			return true
		}
	}
	return false
}

// TestSidecarRoutes_IdentityCoverage is the enumeration gate. Every route
// registered in buildHandler must be classified in sidecarRouteGuards, and
// every classification must hold:
//
//   - guardMemoryChokepoint routes are driven token-less through the real
//     handler and must answer 403;
//   - guardHandlerIdentity routes must reference one of the identity helpers
//     in their handler body (they fail closed there, not at the router);
//   - guardNone routes are the explicitly-accepted remainder.
//
// A new route with no table entry fails here — which is precisely what did not
// happen for /memory/{read,write,search,status,reindex} and is how CRE-153
// survived #1274.
func TestSidecarRoutes_IdentityCoverage(t *testing.T) {
	routes := routeKeysFromSource(t)
	bodies := packageHandlerBodies(t)

	var unclassified []string
	seen := map[string]bool{}
	for _, rt := range routes {
		seen[rt.key] = true
		kind, ok := sidecarRouteGuards[rt.key]
		if !ok {
			unclassified = append(unclassified, rt.key)
			continue
		}
		if kind == guardMemoryChokepoint {
			// Asserted behaviourally, not structurally, by
			// TestSidecarRoutes_MemoryChokepointRefusesTokenless.
			continue
		}
		if _, ok := bodies[rt.handler]; !ok {
			t.Errorf("route %q: handler %q not found in package source", rt.key, rt.handler)
			continue
		}
		resolves := resolvesActingIdentity(rt.handler, bodies, identityWalkDepth)
		switch kind {
		case guardHandlerIdentity:
			if !resolves {
				t.Errorf("route %q is classified guardHandlerIdentity but %s resolves no acting identity",
					rt.key, rt.handler)
			}
		case guardNone:
			// The converse. Without it the table asserts in one direction
			// only: "identity routes do resolve identity". A route that
			// GAINS an identity check keeps its guardNone entry and CI stays
			// green, so the table stops describing the router and starts
			// describing whoever last remembered to edit it. #1757 did
			// exactly that — assignment.go:59 grew an actingAgentID call and
			// "POST /assign" sat at guardNone for a full release, which is
			// how nobody noticed the classification had gone stale.
			//
			// Direction matters more than tidiness here. guardNone is read by
			// the next auditor as "checked; carries no per-agent decision",
			// and that reading is what makes the remaining guardNone rows a
			// short, credible list worth auditing. One row that silently
			// became an identity route devalues every other row on it.
			if resolves {
				t.Errorf("route %q is classified guardNone but %s DOES resolve an acting identity (%v). "+
					"Reclassify it guardHandlerIdentity — guardNone means 'no per-agent identity decision', "+
					"and a stale guardNone is how the table drifts from the router without CI noticing.",
					rt.key, rt.handler, identityHelpers)
			}
		}
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Errorf("routes registered in buildHandler with no entry in sidecarRouteGuards:\n  %s\n"+
			"Add each one with the guard that actually protects it — do NOT default to guardNone "+
			"without checking whether the handler resolves the acting agent.",
			strings.Join(unclassified, "\n  "))
	}
	for key := range sidecarRouteGuards {
		if !seen[key] {
			t.Errorf("sidecarRouteGuards has a stale entry %q — no such route in buildHandler", key)
		}
	}
}

// TestSidecarRoutes_MemoryChokepointRefusesTokenless drives every route
// classified guardMemoryChokepoint token-less, through the real router, on a
// crew with per-agent tokens provisioned. All of them must answer 403.
func TestSidecarRoutes_MemoryChokepointRefusesTokenless(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)

	// Concrete request per classified memory route key. Keys ending in "/"
	// are prefix routes and get a representative slug appended.
	bodies := map[string]string{
		"POST": `{"query":"x","file":"AGENT.md","content":"x\n","jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	}
	var checked int
	for key, kind := range sidecarRouteGuards {
		if kind != guardMemoryChokepoint {
			continue
		}
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Fatalf("malformed route key %q", key)
		}
		if strings.HasSuffix(path, "/") {
			path += "beta"
		}
		if method == http.MethodGet {
			path += "?file=AGENT.md"
		}
		t.Run(key, func(t *testing.T) {
			var body io.Reader
			if b, hasBody := bodies[method]; hasBody {
				body = strings.NewReader(b)
			}
			req := loopbackRequest(method, path, body)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("token-less %s %s → %d, want 403; body=%s",
					method, path, w.Code, w.Body.String())
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatal("no guardMemoryChokepoint routes exercised — table or parser broken")
	}
}

// memoryPerAgentResolvers are the calls that bind a memory request to the
// ACTING agent's own tier, rather than to whichever agent the sidecar booted
// for. memoryAgentContextFor(slug) is the only such resolver today.
var memoryPerAgentResolvers = []string{"memoryAgentContextFor("}

// TestMemoryMCPRoutes_ResolvePerAgentContext pins the assumption the
// cross-agent exemption in refuseUnauthorizedMemory rests on.
//
// The guard refuses an authenticated non-boot member on the LEGACY /memory/*
// routes but exempts the MCP transport, and it keys that exemption on the PATH
// (`!isMemoryMCPPath`). That is sound only because every route under
// /mcp/memory resolves the acting agent's own context — a sibling reaching it
// is served its own tier, so there is nothing to refuse. A route added under
// /mcp/memory/ later inherits the exemption whether or not it does that, and
// would then serve the boot agent's tier to any crew member: exactly the CRE-153
// hole, re-opened through a prefix nobody re-audited.
//
// So: every registered /mcp/memory* route must reach a per-agent resolver.
// Adding one that does not fails here.
func TestMemoryMCPRoutes_ResolvePerAgentContext(t *testing.T) {
	routes := routeKeysFromSource(t)
	bodies := packageHandlerBodies(t)

	var checked int
	for _, rt := range routes {
		_, path, ok := strings.Cut(rt.key, " ")
		if !ok || !isMemoryMCPPath(path) {
			continue
		}
		checked++
		if _, ok := bodies[rt.handler]; !ok {
			t.Errorf("route %q: handler %q not found in package source", rt.key, rt.handler)
			continue
		}
		if !handlerCalls(rt.handler, bodies, 2, memoryPerAgentResolvers) {
			t.Errorf("route %q is exempt from the legacy cross-agent refusal because it is under "+
				"/mcp/memory, but %s never resolves per-agent context (%v). Either resolve the "+
				"acting agent's own tier in that handler, or stop exempting it in "+
				"refuseUnauthorizedMemory.", rt.key, rt.handler, memoryPerAgentResolvers)
		}
	}
	if checked < 2 {
		t.Fatalf("recovered only %d /mcp/memory routes — the parser or the router changed shape", checked)
	}
}
