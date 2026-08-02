package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// route_read_scope_invariant_test.go — the read-side twin of
// route_authz_invariant_test.go.
//
// That file closes the "forgotten role gate" class for MUTATION routes: a new
// POST/PUT/PATCH/DELETE that skips the recording wrapper fails the build. It has
// no read-side equivalent, and that asymmetry is exactly where a cross-tenant
// read leak hides — a new GET registered without wsCtx() reaches its handler
// with no workspace in context, and whether anything is scoped then depends on
// the handler remembering to do it by hand.
//
// That is not hypothetical. The issue assignee leak found in the 2026-07-30 test
// audit was this shape: `assignee_id` accepted unvalidated on write, then joined
// on read with no workspace predicate, so a workspace-A caller could surface a
// workspace-B user's name. A handler-by-handler review found it; no test could
// have, because nothing asserted the *class*.
//
// Why classification instead of "every GET must use wsCtx": some read routes
// legitimately have no workspace. `GET /api/v1/workspaces` lists the caller's
// own memberships — it cannot be workspace-scoped, it is what resolves the
// workspace in the first place. `GET /api/v1/connectors` reads a static
// manifest catalog off disk, not tenant rows. A blanket rule would flag those
// forever and get muted, and a muted invariant is worse than none.
//
// So every read route must land in exactly one bucket:
//
//	wsCtx(...)      — workspace-scoped by the middleware chokepoint. Nothing to do.
//	authedAdmin(...) — same chokepoint plus an ADMIN+ floor, so strictly stronger.
//	internalAuth()  — sidecar trust boundary. Different fence, out of scope here
//	                  (see route_authz_invariant_test.go's own exemption note).
//	allowlisted     — no workspace dimension, with a stated reason below.
//
// Anything else fails. The point is not that the allowlist is short; it is that
// adding to it is a deliberate act with a written reason, instead of the silent
// default.
//
// A note on how the fourth bucket got here, because it is the same mistake this
// file exists to catch. The first version scanned only `r.mux.Handle("GET …")`
// and claimed "every read route lands in exactly one bucket". It did not: the 26
// admin-console reads register as `r.authedAdmin("GET", pattern, h)`, where the
// verb is an ARGUMENT rather than part of the pattern string, so the scan could
// not see them at all — they were in no bucket and nothing failed. A completeness
// claim that quietly excludes a whole surface is exactly the shape of defect this
// test is supposed to prevent, so the scan now covers both registration forms.
//
// authedAdmin is scoped, not excused: rbac_routes.go builds it as
// RequireAuth(RequireWorkspace(requireRoleScopeMW(roleManage, scopeSelf, h))),
// and `wsCtx` is a local alias for that same r.authMw.RequireWorkspace. Same
// chokepoint, plus a role floor.
//
// Verified for completeness rather than assumed: of the five registration forms
// in router_*.go, `r.authedMut(` (246 uses) and `r.authedSelfMut(` (22) carry
// only POST/PUT/PATCH/DELETE — zero reads — so they are genuinely irrelevant
// here rather than a fifth gap. Re-check that if a read ever registers through
// them.

// readVerbLine matches a read-verb registration under /api/v1/. Multi-line
// registrations put the verb and pattern on the first line, which is what this
// keys on — same assumption (and same reason) as mutationVerbLine.
var readVerbLine = regexp.MustCompile(`r\.mux\.(?:Handle|HandleFunc)\("(GET|HEAD) (/api/v1/[^"]*)"`)

// adminReadLine matches the other registration form: the admin console's reads
// go through r.authedAdmin("GET", "/api/v1/…", h), verb as an argument. Missing
// this shape is what hid 26 routes from the first version of this test.
var adminReadLine = regexp.MustCompile(`r\.authedAdmin\("(GET|HEAD)",\s*"(/api/v1/[^"]*)"`)

// readRouteWrapperLookahead caps how many lines after a registration are joined
// before looking for the wrapper. A registration whose handler argument wraps
// onto the next line still has to be classified correctly.
//
// The cap alone is not enough, and getting this wrong is silent. Registrations
// in router_*.go sit one per line, so a fixed window bleeds into the NEXT
// registration — and since the next one usually does carry wsCtx, an ungated
// route reads as gated and the invariant passes. Measured on this tree: 76 of
// 219 read routes had a wsCtx-bearing line within the following three, so a
// fixed window was blind on a third of the surface. Removing wsCtx from
// `GET /api/v1/crews` (router_crews.go:122) passed a fixed-window version of
// this test. So the window also stops at the next registration — see
// nextRegistrationStart.
const readRouteWrapperLookahead = 4

// registrationStart matches the beginning of ANY route registration, not just a
// read one. It bounds the lookahead window: whatever follows belongs to the next
// route, so it must not be read as this route's wrapper.
var registrationStart = regexp.MustCompile(`^\s*r\.(mux\.Handle|mux\.HandleFunc|authedMut|authedSelfMut)\(`)

// readRoutesWithoutWorkspace are the read routes that legitimately carry no
// workspace dimension. Each entry needs a reason a reviewer can check — "it is
// already in the map" is not one.
//
// Adding a route here is a claim that it reads NO workspace-scoped rows. If it
// reads tenant data and derives the workspace inside the handler instead (the
// roleInline equivalent), say so explicitly in the reason, because that shifts
// the guarantee off the chokepoint and onto that handler forever.
var readRoutesWithoutWorkspace = map[string]string{
	// Pre-auth / bootstrap. These run before a session exists, so there is no
	// workspace to scope to and no authed() wrapper either.
	"GET /api/v1/auth/google/status": "pre-auth: the login page renders its Google button from this",
	"GET /api/v1/system/setup-status": "pre-auth: decides whether to redirect a visitor to /bootstrap " +
		"on an empty database",
	"GET /api/v1/system/telemetry": "pre-auth read-only consent state; consent is flipped via the CLI, not HTTP",

	// Caller-scoped: the row set is the caller's own, keyed on user id.
	"GET /api/v1/workspaces":               "caller's own memberships — this is what RESOLVES a workspace, so it cannot be scoped by one",
	"GET /api/v1/auth/sessions":            "caller's own sessions",
	"GET /api/v1/auth/cli-tokens":          "caller's own CLI tokens",
	"GET /api/v1/auth/cli-token/validate":  "validates the caller's own presented token",
	"GET /api/v1/auth/pair/poll":           "polls the caller's own pairing attempt",
	"GET /api/v1/ws-token":                 "mints a websocket token for the caller",
	"GET /api/v1/me/preferences":           "caller's own preferences",
	"GET /api/v1/onboarding/status":        "per-user onboarding progress",
	"GET /api/v1/users/{id}/avatar":        "avatar blob; no tenant rows",
	"GET /api/v1/oauth/callback":           "OAuth redirect landing; state carries the context",
	"GET /api/v1/feedback":                 "caller's own submitted feedback",
	"GET /api/v1/connectors/{connectorId}": "static connector manifest read off disk, not tenant rows",
	"GET /api/v1/mcp-registry/search":      "static registry catalog",

	// Instance-global catalogs and status. No tenant rows involved.
	//
	// The three catalog routes below were surfaced by fixing the window-bleed
	// bug (see readRouteWrapperLookahead) — a fixed window had been borrowing a
	// neighbour's wsCtx and hiding them. Each was checked, not assumed:
	// RecipeHandler.List discards the request entirely (`_ *http.Request`),
	// RecipeHandler.Get holds no workspace reference, and RuntimeCatalogList
	// reads runtimeFetcher.GetRuntimes. The sibling
	// GET /recipes/{slug}/preview DOES carry wsCtx, because a preview resolves
	// against the caller's workspace — catalog global, preview scoped, which is
	// the coherent split rather than an oversight.
	"GET /api/v1/recipes":          "static curated recipe catalog; List discards the request entirely",
	"GET /api/v1/recipes/{slug}":   "one static recipe manifest; no workspace read (contrast: /preview is wsCtx-scoped)",
	"GET /api/v1/runtimes/catalog": "runtime catalog from runtimeFetcher, not tenant rows",

	"GET /api/v1/connectors":       "static connector catalog read off disk, not tenant rows",
	"GET /api/v1/features/catalog": "static devcontainer feature catalog",
	"GET /api/v1/mcp-registry":     "static MCP registry catalog",
	"GET /api/v1/oauth/providers":  "which OAuth providers this instance has configured",
	"GET /api/v1/system/license":   "instance license state",
	"GET /api/v1/system/version":   "instance build version",
	"GET /api/v1/system/runtime":   "instance runtime detail; the handler redacts for non-admins rather than 403-ing (see admin_authz_floor_test.go)",
	// #1668. The handler reads ONE in-memory value: the admission controller's
	// own snapshot. No DB, no query, no workspace column anywhere in the path
	// — the host's free memory and the list of container starts currently held
	// against it are properties of the process and the machine, not of a
	// tenant. The crew ids in the `held` list are the only tenant-adjacent
	// data, and they are there because the whole point of the endpoint is to
	// let the person whose run is waiting see that it is waiting rather than
	// hung; scoping them by workspace would hide a neighbour's crew that is
	// consuming the host's capacity, which is the one thing an operator
	// diagnosing a stall needs to see.
	"GET /api/v1/runtime/capacity": "host admission-control status: an in-memory snapshot of the process " +
		"and the machine, no DB read and no workspace-scoped rows",

	// Workspace derived inside the handler rather than by wsCtx.
	"GET /api/v1/chats/{chatId}/participants": "workspace derived from the chat itself via " +
		"ChatParticipantsHandler.chatWorkspace, then membership-checked — guarantee lives in that handler, not the chokepoint",
	"GET /api/v1/chats/{chatId}/messages/{messageId}/reactions": "same chat-derived scoping as the participants route",
}

// TestEveryReadRouteDeclaresItsWorkspaceScope is the invariant: a read route is
// either workspace-scoped by the chokepoint, on the internal trust boundary, or
// explicitly excused above. A new GET that is none of those fails here.
func TestEveryReadRouteDeclaresItsWorkspaceScope(t *testing.T) {
	routes := scanReadRoutes(t)

	var unclassified []string
	for _, rt := range routes {
		switch {
		case rt.adminWrapped:
			continue // RequireWorkspace + ADMIN+ floor, composed by the wrapper
		case strings.Contains(rt.tail, "internalAuth("):
			continue // sidecar boundary — different fence
		case strings.Contains(rt.tail, "wsCtx("):
			continue // scoped by the chokepoint
		}
		if _, ok := readRoutesWithoutWorkspace[rt.key()]; ok {
			continue
		}
		unclassified = append(unclassified, formatOffender(rt.file, rt.line, rt.key()))
	}

	if len(unclassified) > 0 {
		t.Fatalf(`%d read route(s) are neither workspace-scoped nor declared workspace-free:
%s

Fix one of these ways:
  - wrap it in wsCtx(...) so the workspace comes from the chokepoint (preferred), or
  - add it to readRoutesWithoutWorkspace in this file WITH a reason explaining
    why it reads no workspace-scoped rows.
Do not add it to the map to make this test pass — the map is a review record.`,
			len(unclassified), strings.Join(unclassified, "\n"))
	}
}

// TestReadRouteScanFindsTheRealSurface guards the guard. If the registration
// shape changes and readVerbLine stops matching, the test above passes
// vacuously — it would find nothing to complain about and report success. This
// is the more important of the two tests.
func TestReadRouteScanFindsTheRealSurface(t *testing.T) {
	routes := scanReadRoutes(t)

	// 245 read routes on the tree this landed against: 219 through
	// r.mux.Handle("GET …") and 26 through r.authedAdmin("GET", …). A floor well
	// under that catches regex rot without tripping on ordinary growth.
	//
	// Both forms are counted separately below, because a single total would hide
	// one regex rotting while the other still matched — which is how the admin
	// surface went unnoticed in the first place.
	const minRoutes = 200
	if len(routes) < minRoutes {
		t.Fatalf("scanned only %d read routes, expected at least %d — a registration regex has likely stopped matching, which would make TestEveryReadRouteDeclaresItsWorkspaceScope pass vacuously",
			len(routes), minRoutes)
	}

	admin := 0
	for _, rt := range routes {
		if rt.adminWrapped {
			admin++
		}
	}
	const minAdminRoutes = 15
	if admin < minAdminRoutes {
		t.Errorf("scanned only %d authedAdmin read routes, expected at least %d — adminReadLine has likely stopped matching, which silently drops the whole admin-console read surface from this invariant",
			admin, minAdminRoutes)
	}

	// The overwhelming majority must be chokepoint-scoped. If this ratio ever
	// inverts, the codebase has drifted to hand-rolled scoping and the
	// allowlist has become the norm instead of the exception. authedAdmin counts
	// as scoped — it composes the same RequireWorkspace, plus a role floor.
	scoped := 0
	for _, rt := range routes {
		if rt.adminWrapped || strings.Contains(rt.tail, "wsCtx(") {
			scoped++
		}
	}
	if scoped*100/len(routes) < 60 {
		t.Errorf("only %d/%d read routes (%d%%) are scoped by the chokepoint — workspace scoping is drifting into handlers",
			scoped, len(routes), scoped*100/len(routes))
	}

	// Every allowlist entry must still correspond to a real route. A stale
	// entry is a route that was deleted or renamed, and it silently excuses
	// nothing — or worse, excuses a future route that reuses the pattern.
	live := make(map[string]bool, len(routes))
	for _, rt := range routes {
		live[rt.key()] = true
	}
	for key := range readRoutesWithoutWorkspace {
		if !live[key] {
			t.Errorf("readRoutesWithoutWorkspace has a stale entry %q — no such read route is registered; remove it", key)
		}
	}

	// A reason is the whole point of the map. An empty one is not a reason.
	for key, reason := range readRoutesWithoutWorkspace {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("readRoutesWithoutWorkspace[%q] has no reason", key)
		}
	}
}

type readRoute struct {
	file string
	line int
	verb string
	path string
	tail string
	// adminWrapped means it registered through r.authedAdmin, which composes
	// RequireWorkspace + an ADMIN+ floor — scoped by the chokepoint, so it needs
	// neither a wsCtx match nor an allowlist entry.
	adminWrapped bool
}

func (r readRoute) key() string { return r.verb + " " + r.path }

func scanReadRoutes(t *testing.T) []readRoute {
	t.Helper()

	routerFiles, err := filepath.Glob("router_*.go")
	if err != nil {
		t.Fatalf("glob router files: %v", err)
	}
	if len(routerFiles) == 0 {
		t.Fatal("no router_*.go files found — test is looking in the wrong directory")
	}

	var routes []readRoute
	scanned := 0
	for _, f := range routerFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			// authedAdmin composes RequireWorkspace itself, so it needs no
			// wrapper lookahead — the registration line is the whole story.
			if m := adminReadLine.FindStringSubmatch(line); m != nil {
				routes = append(routes, readRoute{
					file: f, line: i + 1, verb: m[1], path: m[2],
					tail: line, adminWrapped: true,
				})
				continue
			}
			m := readVerbLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			end := i + readRouteWrapperLookahead
			if end > len(lines) {
				end = len(lines)
			}
			// Stop before the next registration so its wrapper is not read as
			// this one's. Without this the window bleeds and an ungated route
			// borrows its neighbour's wsCtx — see readRouteWrapperLookahead.
			for j := i + 1; j < end; j++ {
				if registrationStart.MatchString(lines[j]) {
					end = j
					break
				}
			}
			routes = append(routes, readRoute{
				file: f,
				line: i + 1,
				verb: m[1],
				path: m[2],
				tail: strings.Join(lines[i:end], " "),
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test router_*.go files were scanned")
	}
	return routes
}
