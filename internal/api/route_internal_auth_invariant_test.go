package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// route_internal_auth_invariant_test.go — closes the exemption that
// route_authz_invariant_test.go carves out for the sidecar's X-Internal-Token
// surface ("a different trust boundary, uniformly mediated by their own
// single wrapper, and are out of scope here by design"). That exemption was
// true by inspection, not by construction: nothing stopped a new
// /api/v1/internal/* registration from landing without requireInternal.
//
// This is the same class of bug the JWT-side invariant fixes for
// authedMut/authedSelfMut, just on the other trust boundary: today all 55
// internal routes go through the `internalAuth` wrapper
// (registerInternalRoutes builds one `internalAuth := internal.requireInternal`
// and reuses it — see the comment at internal.go's requireInternal), and
// that surface is the most sensitive one in the product — it's how an
// in-container agent pulls credentials, mints issues, reports memory, and
// spawns hires. Today it holds together purely by review discipline: a
// copy-paste registration or a refactor that drops the wrapper compiles,
// passes review unless someone reads the diff character-by-character, and
// opens the sidecar token surface to the world with zero test signal. These
// two tests make "every /api/v1/internal/* registration is wrapped in
// internalAuth" a build-time property instead of a code-review hope.
//
// internalRouteLine matches a /api/v1/internal/* ServeMux registration in the
// router source (single line; multi-line registrations put the verb+pattern
// on the first line — same convention mutationVerbLine documents in
// route_authz_invariant_test.go, and for the same reason: that's the line we
// can reliably key the auth-wrapper check on).
var internalRouteLine = regexp.MustCompile(`r\.mux\.(Handle|HandleFunc)\("((?:GET|POST|PUT|PATCH|DELETE) [^"]*/api/v1/internal/[^"]*)"`)

// internalRouteMatch is one /api/v1/internal/* registration found in the
// router source.
type internalRouteMatch struct {
	file    string
	line    int
	route   string // "METHOD /api/v1/internal/...", captured from the source
	gated   bool   // line carries the internalAuth( wrapper
	rawLine string
}

// findInternalRoutes scans every non-test router_*.go file for
// /api/v1/internal/* registrations. Scanning all router_*.go files (not just
// router_internal.go) mirrors TestNoLegacyAuthedMutationRegistration's glob:
// today every internal route happens to live in router_internal.go, but the
// invariant this test enforces is about the route table, not about which
// file it's declared in — a future internal route wired from a different
// router_*.go file must be caught the same way.
func findInternalRoutes(t *testing.T) []internalRouteMatch {
	t.Helper()

	routerFiles, err := filepath.Glob("router_*.go")
	if err != nil {
		t.Fatalf("glob router files: %v", err)
	}
	if len(routerFiles) == 0 {
		t.Fatal("no router_*.go files found — test is looking in the wrong directory")
	}

	var matches []internalRouteMatch
	checked := 0
	for _, f := range routerFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			m := internalRouteLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			matches = append(matches, internalRouteMatch{
				file:    f,
				line:    i + 1,
				route:   m[2],
				gated:   strings.Contains(line, "internalAuth("),
				rawLine: line,
			})
		}
	}
	if checked == 0 {
		t.Fatal("no non-test router_*.go files were scanned")
	}
	return matches
}

// TestEveryInternalRouteRequiresInternalAuth is the source guard: it fails
// if any /api/v1/internal/* registration is missing the internalAuth(...)
// wrapper around requireInternal. This is what turns a dropped auth wrapper
// (copy-paste, refactor) into a BUILD FAILURE instead of a silent hole in
// the sidecar's trust boundary that a reviewer has to notice by eye.
//
// RED check performed by hand while writing this test: removing internalAuth(
// from a single registration in router_internal.go turned this test red with
// the offending file:line and route name, then green again once reverted —
// see the PR description for which route and what the failure looked like.
func TestEveryInternalRouteRequiresInternalAuth(t *testing.T) {
	routes := findInternalRoutes(t)

	var offenders []string
	for _, m := range routes {
		if !m.gated {
			offenders = append(offenders, formatInternalOffender(m))
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d /api/v1/internal/* route(s) registered without the internalAuth(...) wrapper — every internal route must run through requireInternal (the sidecar's X-Internal-Token gate) at registration:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

func formatInternalOffender(m internalRouteMatch) string {
	return "  " + m.file + ":" + strconv.Itoa(m.line) + ": " + m.route + ": " + strings.TrimSpace(m.rawLine)
}

// TestInternalRouteTableIsNotEmpty guards the guard: if a future refactor
// changes how internal routes are registered (e.g. moves off r.mux.Handle,
// or reshapes the pattern string) such that internalRouteLine stops matching
// anything, TestEveryInternalRouteRequiresInternalAuth would pass vacuously
// and silently stop meaning anything. Without this, the invariant above is
// worthless — you can't tell "no ungated routes" from "found no routes at
// all". The product currently registers 55 internal routes; 40 leaves
// headroom for incidental drift while still catching the file-goes-missing
// or regex-stops-matching case.
func TestInternalRouteTableIsNotEmpty(t *testing.T) {
	routes := findInternalRoutes(t)
	if len(routes) < 40 {
		t.Fatalf("only %d /api/v1/internal/* route(s) found; expected the full surface (~55) — internalRouteLine may no longer match the current registration style, which would make TestEveryInternalRouteRequiresInternalAuth pass vacuously", len(routes))
	}
}
