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
// Scope: the JWT-session mutation surface — routes that used to register as
// `authed(wsCtx(...))` (workspace-scoped) or bare `authed(...)` (self-scoped).
// That is exactly the class where the vulnerability lives: a workspace member
// reaching a handler that forgot its inline check. The X-Internal-Token
// sidecar surface (`internalAuth(...)`) and the public token/HMAC dispatch
// routes (webhooks, waitpoint tokens, bootstrap/signup) are a different trust
// boundary, uniformly mediated by their own single wrapper, and are out of
// scope here by design.

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

// mutationVerbLine matches a mutation-verb ServeMux registration in the router
// source (single line; multi-line registrations put the verb on the first
// line, which is what we key on).
var mutationVerbLine = regexp.MustCompile(`r\.mux\.(Handle|HandleFunc)\("(POST|PUT|PATCH|DELETE) `)

// TestNoLegacyAuthedMutationRegistration is the source guard: it fails if any
// mutation route is still registered through the old `authed(...)` chain
// instead of the recording wrappers. This is what makes adding a new ungated
// mutation a BUILD FAILURE rather than a silent omission a reviewer must catch.
//
// It keys on the textual wrapper: `authed(` (which covers both
// `authed(wsCtx(...))` and bare `authed(...)`) must never appear on a mutation
// registration — those must be `r.authedMut(...)` / `r.authedSelfMut(...)`,
// which do not contain the substring `authed(`. The internal sidecar wrapper
// `internalAuth(` and public `HandleFunc` token routes are deliberately
// allowed (different, uniformly-mediated trust boundary).
//
// RED before the migration: ~200 `r.mux.Handle("POST ...", authed(wsCtx(...)))`
// lines still exist.
func TestNoLegacyAuthedMutationRegistration(t *testing.T) {
	routerFiles, err := filepath.Glob("router_*.go")
	if err != nil {
		t.Fatalf("glob router files: %v", err)
	}
	if len(routerFiles) == 0 {
		t.Fatal("no router_*.go files found — test is looking in the wrong directory")
	}

	var offenders []string
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
			if !mutationVerbLine.MatchString(line) {
				continue
			}
			// A mutation registration must go through a recording wrapper.
			// The legacy chain leaves the literal `authed(` on the line.
			if strings.Contains(line, "authed(") {
				offenders = append(offenders, formatOffender(f, i+1, line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-test router_*.go files were scanned")
	}
	if len(offenders) > 0 {
		t.Fatalf("%d mutation route(s) still registered via the legacy authed(...) chain instead of authedMut/authedSelfMut — every mutation route must declare a role at registration:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
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
			mw := r.requireRoleMW(c.role, h)
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
