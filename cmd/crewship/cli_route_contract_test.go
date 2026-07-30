package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// CLI↔route contract invariants (#1576).
//
// Two of the three ways a command drifts from its handler are decidable from
// source alone, so they should be a build failure rather than a bug report:
//
//   - the command calls a {method, path} the router does not register
//     (a moved or renamed route);
//   - the command clears its workspace before calling a route that sits
//     behind RequireWorkspace (#896 — `system keeper`, and again, unnoticed,
//     on `system aux-status` in #1514).
//
// The third — response shape — cannot be settled statically, because the
// handler's response literal and the CLI's decode struct are unrelated types.
// That one is covered per-command in cmd_handler_drift_test.go, and this file
// is the reason the other two never need a per-command test.
//
// Both invariants walk real source: the CLI's call sites via go/ast, the
// router's table via the registration helpers in internal/api. Nothing is
// hand-maintained, so a new command or a new route is covered the moment it
// is written.

// ─── shared plumbing ─────────────────────────────────────────────────────

// httpVerbs are the client methods a command uses to reach the API. Get/Post/
// Put/Patch/Delete on *cli.Client all funnel into Client.do.
var httpVerbs = map[string]string{
	"Get": "GET", "Post": "POST", "Put": "PUT", "Patch": "PATCH", "Delete": "DELETE",
}

// knownVerb reports whether s is one of the wire methods above. Used to tell
// a `mux.Handle("GET /api/…")` route spec apart from the many other single
// -string Handle calls in the tree (metrics, SPA catch-alls, /.well-known).
func knownVerb(s string) bool {
	for _, v := range httpVerbs {
		if s == v {
			return true
		}
	}
	return false
}

// cliCallSite is one `client.<Verb>("/api/v1/…")` in the CLI.
type cliCallSite struct {
	Method string
	Path   string // normalised: dynamic segments as {}
	Raw    string
	Pos    string
	// ClearsWorkspace records that the enclosing function assigns
	// `…WorkspaceID = ""` before the call — the #896 shape.
	ClearsWorkspace bool
}

// apiRoute is one registration in internal/api.
type apiRoute struct {
	Method            string
	Pattern           string // normalised: {id} → {}
	RequiresWorkspace bool
	Pos               string
}

// repoRoot resolves the module root from the package directory the test runs in.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod: %v", root, err)
	}
	return root
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out
}

// suffixFragment matches a `{}` that is not its own path segment — the tail of
// `"/api/v1/models" + queryString(...)` or a `%s` query appended to a path.
// Those are query strings, not route parameters, and the router never sees them.
var suffixFragment = regexp.MustCompile(`([^/])\{\}`)

var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// normalisePath collapses a rendered path expression to the form the router
// registers: every dynamic path segment becomes `{}`, query strings and
// trailing non-segment interpolations are dropped.
func normalisePath(p string) string {
	if i := strings.IndexAny(p, "?"); i >= 0 {
		p = p[:i]
	}
	p = routeParam.ReplaceAllString(p, "{}")
	for {
		next := suffixFragment.ReplaceAllString(p, "$1")
		if next == p {
			break
		}
		p = next
	}
	for strings.Contains(p, "{}{}") {
		p = strings.ReplaceAll(p, "{}{}", "{}")
	}
	return strings.TrimSuffix(p, "/")
}

// renderPathExpr turns a Go expression into a path template: string literals
// contribute verbatim, everything else is an opaque `{}`. fmt.Sprintf is read
// through its format string, whose verbs are the dynamic segments.
func renderPathExpr(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "{}", true
		}
		l, okL := renderPathExpr(v.X)
		if !okL {
			l = "{}"
		}
		r, okR := renderPathExpr(v.Y)
		if !okR {
			r = "{}"
		}
		return l + r, true
	case *ast.CallExpr:
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" && len(v.Args) > 0 {
				if f, ok := renderPathExpr(v.Args[0]); ok {
					return regexp.MustCompile(`%[-+ #0-9.]*[a-zA-Z]`).ReplaceAllString(f, "{}"), true
				}
			}
		}
		return "{}", true
	case *ast.ParenExpr:
		return renderPathExpr(v.X)
	default:
		return "{}", true
	}
}

// collectCLICallSites walks the CLI packages and returns every API call the
// binary can make, tagged with whether its enclosing function cleared the
// workspace first.
func collectCLICallSites(t *testing.T) []cliCallSite {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()

	var sites []cliCallSite
	for _, dir := range []string{
		filepath.Join(root, "cmd", "crewship"),
		filepath.Join(root, "internal", "cli"),
	} {
		for _, path := range goFiles(t, dir) {
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			// Walk every function-shaped body separately so "did this
			// function clear the workspace" is scoped, not file-global.
			ast.Inspect(f, func(n ast.Node) bool {
				var body *ast.BlockStmt
				switch fn := n.(type) {
				case *ast.FuncDecl:
					body = fn.Body
				case *ast.FuncLit:
					body = fn.Body
				default:
					return true
				}
				if body == nil {
					return true
				}
				sites = append(sites, callSitesInBody(fset, body)...)
				return true
			})
		}
	}
	return sites
}

// callSitesInBody extracts the API calls in one function body. Nested function
// literals are visited by the outer walk too; the duplicate is harmless
// because both copies assert the same thing.
func callSitesInBody(fset *token.FileSet, body *ast.BlockStmt) []cliCallSite {
	cleared := false
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		sel, ok := as.Lhs[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WorkspaceID" {
			return true
		}
		lit, ok := as.Rhs[0].(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && lit.Value == `""` {
			cleared = true
		}
		return true
	})

	var out []cliCallSite
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method, ok := httpVerbs[sel.Sel.Name]
		if !ok {
			return true
		}
		raw, ok := renderPathExpr(call.Args[0])
		if !ok || !strings.HasPrefix(raw, "/api/") {
			return true
		}
		out = append(out, cliCallSite{
			Method:          method,
			Path:            normalisePath(raw),
			Raw:             raw,
			Pos:             fset.Position(call.Pos()).String(),
			ClearsWorkspace: cleared,
		})
		return true
	})
	return out
}

// collectAPIRoutes reads the router's registration helpers. authedMut and
// authedAdmin both wrap RequireAuth(RequireWorkspace(...)) — see
// internal/api/rbac_routes.go — so registration alone tells us whether a
// workspace is mandatory. authedSelfMut is RequireAuth only.
func collectAPIRoutes(t *testing.T) map[string]apiRoute {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	routes := map[string]apiRoute{}

	litString := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}

	add := func(method, pattern string, ws bool, pos string) {
		key := method + " " + normalisePath(pattern)
		if prev, seen := routes[key]; seen {
			// Two registrations of one route: keep the stricter gate so the
			// workspace invariant cannot be satisfied by the laxer twin.
			ws = ws || prev.RequiresWorkspace
		}
		routes[key] = apiRoute{Method: method, Pattern: normalisePath(pattern), RequiresWorkspace: ws, Pos: pos}
	}

	for _, path := range goFiles(t, filepath.Join(root, "internal", "api")) {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := strings.Split(string(src), "\n")

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pos := fset.Position(call.Pos())
			switch sel.Sel.Name {
			case "authedMut", "authedAdmin", "authedSelfMut":
				if len(call.Args) < 2 {
					return true
				}
				method, okM := litString(call.Args[0])
				pattern, okP := litString(call.Args[1])
				if !okM || !okP {
					return true
				}
				add(method, pattern, sel.Sel.Name != "authedSelfMut", pos.String())
			case "Handle", "HandleFunc":
				if len(call.Args) < 1 {
					return true
				}
				spec, ok := litString(call.Args[0])
				if !ok {
					return true
				}
				parts := strings.SplitN(spec, " ", 2)
				if len(parts) != 2 || !knownVerb(parts[0]) {
					return true
				}
				// Gate is not in the AST shape here; read the registration
				// line, which names the middleware chain inline.
				ws := false
				if pos.Line-1 < len(lines) {
					// The chain can wrap onto the following line.
					window := lines[pos.Line-1]
					if pos.Line < len(lines) {
						window += lines[pos.Line]
					}
					ws = strings.Contains(window, "RequireWorkspace") || strings.Contains(window, "wsCtx")
				}
				add(strings.ToUpper(parts[0]), parts[1], ws, pos.String())
			}
			return true
		})
	}
	if len(routes) < 200 {
		t.Fatalf("route table looks truncated (%d routes) — the extractor stopped matching the router's registration helpers, "+
			"which would make both invariants below vacuously true", len(routes))
	}
	return routes
}

// dynamicPathExceptions are call sites whose final segment is chosen at
// runtime, so no single route pattern can be matched statically. Each entry is
// a normalised "METHOD /path" and must name why it cannot be resolved.
var dynamicPathExceptions = map[string]string{
	// `action` is one of the governance verbs (promote/rollback/…); each is
	// its own registered route, and the command picks between them at runtime.
	"POST /api/v1/workspaces/{}/pipelines/{}/{}": "governance action is a runtime-chosen verb",
}

// ─── invariant 1: every CLI call reaches a route that exists ─────────────

func TestCLICallsHitRegisteredRoutes(t *testing.T) {
	routes := collectAPIRoutes(t)
	sites := collectCLICallSites(t)
	if len(sites) < 100 {
		t.Fatalf("CLI call-site extractor found only %d call sites — it stopped matching, "+
			"so this invariant would pass without checking anything", len(sites))
	}

	seen := map[string]bool{}
	for _, s := range sites {
		key := s.Method + " " + s.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		if _, ok := routes[key]; ok {
			continue
		}
		if why, ok := dynamicPathExceptions[key]; ok {
			t.Logf("skipping %s (%s)", key, why)
			continue
		}
		// Name the near-miss: a verb change is the common drift, and saying
		// so turns "route not found" into an actionable diff.
		var others []string
		for k := range routes {
			if strings.HasSuffix(k, " "+s.Path) {
				others = append(others, k)
			}
		}
		sort.Strings(others)
		hint := "no route with this path is registered at all"
		if len(others) > 0 {
			hint = "the path exists under a different method: " + strings.Join(others, ", ")
		}
		t.Errorf("%s calls %s %s (source expression %q) but the router does not register it — %s",
			s.Pos, s.Method, s.Path, s.Raw, hint)
	}
}

// ─── invariant 2: don't clear the workspace on a workspace-gated route ───

// knownWorkspaceClearingDrift are violations already being fixed on another
// branch. An entry here is a promise, not an excuse: the test fails if one
// stops matching, so a merged fix forces the entry out instead of leaving a
// permanent hole. Never add an entry for a bug nobody is fixing.
var knownWorkspaceClearingDrift = map[string]string{
	"GET /api/v1/system/aux-status": "#1514, fixed by PR #1564 (fix/cli-aux-status-subsystems) — delete this entry when it merges",
}

func TestCLIDoesNotClearWorkspaceOnWorkspaceGatedRoutes(t *testing.T) {
	routes := collectAPIRoutes(t)
	sites := collectCLICallSites(t)

	used := map[string]bool{}
	reported := map[string]bool{}
	for _, s := range sites {
		if !s.ClearsWorkspace {
			continue
		}
		key := s.Method + " " + s.Path
		r, ok := routes[key]
		if !ok || !r.RequiresWorkspace {
			continue
		}
		if why, known := knownWorkspaceClearingDrift[key]; known {
			used[key] = true
			t.Logf("known drift, tracked elsewhere: %s at %s (%s)", key, s.Pos, why)
			continue
		}
		if reported[s.Pos] {
			continue
		}
		reported[s.Pos] = true
		t.Errorf("%s clears client.WorkspaceID and then calls %s %s, which is registered behind "+
			"RequireWorkspace (%s). Every invocation 400s before the response shape even matters — "+
			"this is #896. Drop the `client.WorkspaceID = \"\"` and call requireWorkspace() instead.",
			s.Pos, s.Method, s.Path, r.Pos)
	}

	for key, why := range knownWorkspaceClearingDrift {
		if !used[key] {
			t.Errorf("stale exception for %q (%s): the violation is gone, so the entry is now hiding "+
				"nothing and would silently absolve a future regression. Delete it from "+
				"knownWorkspaceClearingDrift.", key, why)
		}
	}
}
