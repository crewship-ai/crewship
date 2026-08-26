package main

import (
	"fmt"
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
// hand-maintained, so a new route is covered the moment it is written.
//
// WHAT THE EXTRACTOR SEES (#2086 closed the three gaps it used to have). A
// call site is examined when its path argument RESOLVES to a string starting
// with `/api/`, and "resolves" now covers:
//
//	// (a) the direct methods on *cli.Client
//	resp, err := client.Get("/api/v1/agents")
//	resp, err := client.Do(http.MethodPut, "/api/v1/instance/settings/"+k, b)
//	err := client.StreamNDJSON(ctx, chatStreamPath(id, seq, opts), "", fn)
//
//	// (b) the plain-function wrappers in api_helpers.go, which take the
//	//     path as their SECOND argument
//	getJSON(client, "/api/v1/agents/"+slug, &out)
//
//	// (c) the path assembled into a local first — every assignment to the
//	//     name in the enclosing body is a CANDIDATE, and every candidate is
//	//     checked, so a path chosen by an if/else is checked in both arms
//	path := "/api/v1/inbox" + queryString("unread", u)
//	if archived { path = "/api/v1/inbox/archived" }
//	resp, err := client.Get(path)
//
//	// (d) package-level path consts, and helper funcs that RETURN a path
//	const keeperAuxPath = "/api/v1/admin/keeper/aux"
//	func gdprUserPath(c *cli.Client, id string) string { … }
//
//	// (e) forwarders — a func that hands one of its own string params to
//	//     any of the above. Discovered from source, not listed here, so
//	//     putBytes / postMultipart / cuidFetch / getByRef are covered and
//	//     the next one is covered the day it is written.
//
// That takes the extractor from ~450 call sites to ~950, and the
// `len(sites) < minCallSites` vacuity guard below moves with it.
//
// What is still opaque, and why it does not silently pass:
//
//   - a segment interpolated by fmt.Sprintf renders as `{}` whatever the
//     argument is, so a runtime-chosen VERB (`…/hooks/{id}/{enable|disable}`)
//     collapses to `{}` and cannot be matched against a route pattern. Those
//     sites need a dynamicPathExceptions entry naming the registered routes.
//   - a local whose every assignment is a call we cannot see through renders
//     as `{}` and is dropped by the `/api/` prefix filter. That is a missed
//     check, never a false pass of a wrong path.

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

// ─── call shapes ─────────────────────────────────────────────────────────
//
// A callShape says where in an argument list the wire method and the path
// live. Templates carry `«argN»` holes that the call site's own arguments
// fill, which is what lets one shape describe both a direct
// `getJSON(client, path, &out)` and a forwarder like
// `getByRef(client, "/api/v1/agents/", ref, resolve)` that concatenates two
// of its params.
type callShape struct {
	Method    string // fixed wire method; "" → read it from MethodArg
	MethodArg int
	Templates []string
	MinArgs   int
}

// argHole is the `«argN»` placeholder written above. NUL-delimited so it
// cannot collide with anything a real path expression renders to.
const argHole = "\x00arg%d\x00"

func hole(i int) string { return fmt.Sprintf(argHole, i) }

var holeRE = regexp.MustCompile("\x00arg([0-9]+)\x00")

// clientMethodShapes are the *cli.Client methods a command reaches the API
// through. Matched on the selector name alone — the `/api/` prefix filter is
// what keeps `q.Get("origin")` and `sync.Once.Do(fn)` out.
var clientMethodShapes = map[string]callShape{
	"Get":    {Method: "GET", Templates: []string{hole(0)}, MinArgs: 1},
	"Post":   {Method: "POST", Templates: []string{hole(0)}, MinArgs: 1},
	"Put":    {Method: "PUT", Templates: []string{hole(0)}, MinArgs: 1},
	"Patch":  {Method: "PATCH", Templates: []string{hole(0)}, MinArgs: 1},
	"Delete": {Method: "DELETE", Templates: []string{hole(0)}, MinArgs: 1},
	// Do(method, path, body) and NewRequest(ctx, method, path, body) name
	// their own verb; MinArgs keeps http.NewRequest(method, url, body) —
	// three args, and not our API — out.
	"Do":         {MethodArg: 0, Templates: []string{hole(1)}, MinArgs: 3},
	"NewRequest": {MethodArg: 1, Templates: []string{hole(2)}, MinArgs: 4},
	// Both streams build a GET internally (internal/cli/sse.go, ndjson.go).
	"StreamSSE":    {Method: "GET", Templates: []string{hole(1)}, MinArgs: 2},
	"StreamNDJSON": {Method: "GET", Templates: []string{hole(1)}, MinArgs: 2},
}

// wrapperShapes are the plain-function wrappers in api_helpers.go. They are
// not methods, and the path is their SECOND argument.
var wrapperShapes = map[string]callShape{
	"getJSON":    {Method: "GET", Templates: []string{hole(1)}, MinArgs: 2},
	"postJSON":   {Method: "POST", Templates: []string{hole(1)}, MinArgs: 2},
	"patchJSON":  {Method: "PATCH", Templates: []string{hole(1)}, MinArgs: 2},
	"putJSON":    {Method: "PUT", Templates: []string{hole(1)}, MinArgs: 2},
	"deleteJSON": {Method: "DELETE", Templates: []string{hole(1)}, MinArgs: 2},
}

// ─── path resolution ─────────────────────────────────────────────────────

// maxCandidates caps the fan-out of a concatenation whose operands each have
// several candidates. Nothing in the tree comes close; the cap exists so a
// pathological future call site cannot make this test quadratic.
const maxCandidates = 24

var sprintfVerb = regexp.MustCompile(`%[-+ #0-9.]*[a-zA-Z]`)

// scope resolves an identifier to the strings it can hold at a call site.
// `exprs` are the assignments seen in the enclosing function body (every
// assignment is a candidate — this is flow-insensitive on purpose, so an
// if/else that picks between two paths yields both); `fixed` is used while
// discovering forwarders, where a func's own params stand in as `«argN»`.
type scope struct {
	exprs map[string][]ast.Expr
	fixed map[string][]string
}

// pathResolver renders path expressions. It carries the package-level facts
// that a single expression cannot supply on its own: path consts, funcs that
// return a path, and the request-issuing shapes (wrappers plus every
// forwarder discovered in the tree).
type pathResolver struct {
	globals map[string][]ast.Expr // package-level const/var
	helpers map[string][]string   // func name → paths it can return
	shapes  map[string]callShape  // func name → how it issues a request
}

// render turns an expression into every path string it can produce.
func (r *pathResolver) render(e ast.Expr, sc *scope, seen map[string]bool) []string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return []string{"{}"}
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return []string{"{}"}
		}
		return []string{s}

	case *ast.Ident:
		if sc != nil {
			if fixed, ok := sc.fixed[v.Name]; ok {
				return fixed
			}
		}
		var defs []ast.Expr
		if sc != nil {
			defs = sc.exprs[v.Name]
		}
		if len(defs) == 0 {
			defs = r.globals[v.Name]
		}
		if len(defs) == 0 || seen[v.Name] {
			return []string{"{}"}
		}
		seen[v.Name] = true
		defer delete(seen, v.Name)
		var out []string
		for _, d := range defs {
			for _, c := range r.render(d, sc, seen) {
				// A name that can hold "" is a name whose value is
				// supplied at runtime (`bindingID := ""` then filled
				// from a flag or a lookup). Letting the empty candidate
				// through would turn `"/…/bindings/" + bindingID` into
				// a path with a segment missing and report the call as
				// unregistered — a false positive on correct code.
				if c == "" {
					c = "{}"
				}
				out = append(out, c)
			}
		}
		return dedupCandidates(out)

	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return []string{"{}"}
		}
		left := r.render(v.X, sc, seen)
		right := r.render(v.Y, sc, seen)
		var out []string
		for _, l := range left {
			for _, rr := range right {
				if len(out) >= maxCandidates {
					return dedupCandidates(out)
				}
				out = append(out, l+rr)
			}
		}
		return dedupCandidates(out)

	case *ast.ParenExpr:
		return r.render(v.X, sc, seen)

	case *ast.CallExpr:
		// fmt.Sprintf is read through its format string; each verb is a
		// dynamic segment. Deliberately NOT substituting the arguments:
		// a runtime-chosen segment must stay `{}` so it is reported as
		// unresolvable rather than matched against one of its values.
		if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" && len(v.Args) > 0 {
				var out []string
				for _, f := range r.render(v.Args[0], sc, seen) {
					out = append(out, sprintfVerb.ReplaceAllString(f, "{}"))
				}
				return dedupCandidates(out)
			}
		}
		// A helper whose whole job is to build a path.
		if id, ok := v.Fun.(*ast.Ident); ok {
			if paths, ok := r.helpers[id.Name]; ok && !seen["func:"+id.Name] {
				seen["func:"+id.Name] = true
				defer delete(seen, "func:"+id.Name)
				return paths
			}
		}
		return []string{"{}"}

	default:
		return []string{"{}"}
	}
}

func dedupCandidates(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxCandidates {
			break
		}
	}
	if len(out) == 0 {
		return []string{"{}"}
	}
	return out
}

// renderMethod resolves the wire method of a shape that names its own verb:
// a literal, an `http.MethodX` constant, or a local/const holding either.
func (r *pathResolver) renderMethod(e ast.Expr, sc *scope) []string {
	var out []string
	add := func(s string) {
		s = strings.ToUpper(s)
		if knownVerb(s) {
			out = append(out, s)
		}
	}
	switch v := e.(type) {
	case *ast.SelectorExpr:
		// http.MethodGet, http.MethodPut, …
		if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "http" && strings.HasPrefix(v.Sel.Name, "Method") {
			add(strings.TrimPrefix(v.Sel.Name, "Method"))
		}
	default:
		for _, c := range r.render(e, sc, map[string]bool{}) {
			add(c)
		}
	}
	return dedupMethods(out)
}

// dedupMethods is dedupCandidates without the "{}" floor — an empty
// method list means "unresolvable", which the caller must not treat as a verb.
func dedupMethods(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// substitute fills a shape's `«argN»` holes from the call's own arguments.
//
// Single left-to-right pass over the template's holes, never re-scanning what
// it just wrote: while discovering forwarders an argument RENDERS to a hole
// (that is how one forwarder composes onto another), and re-scanning the
// output would substitute that hole into itself forever.
func (r *pathResolver) substitute(tmpl string, args []ast.Expr, sc *scope) []string {
	holes := holeRE.FindAllStringSubmatchIndex(tmpl, -1)
	if len(holes) == 0 {
		return []string{tmpl}
	}
	cur := []string{""}
	last := 0
	for _, m := range holes {
		lit := tmpl[last:m[0]]
		last = m[1]
		idx, err := strconv.Atoi(tmpl[m[2]:m[3]])
		if err != nil {
			return []string{"{}"}
		}
		fill := []string{"{}"}
		if idx < len(args) {
			fill = r.render(args[idx], sc, map[string]bool{})
		}
		var next []string
		for _, c := range cur {
			for _, f := range fill {
				if len(next) >= maxCandidates {
					break
				}
				next = append(next, c+lit+f)
			}
		}
		cur = dedupCandidates(next)
	}
	tail := tmpl[last:]
	out := make([]string, 0, len(cur))
	for _, c := range cur {
		out = append(out, c+tail)
	}
	return dedupCandidates(out)
}

// shapeOf reports how a call issues its request, if it does.
func (r *pathResolver) shapeOf(call *ast.CallExpr) (callShape, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if s, ok := clientMethodShapes[fn.Sel.Name]; ok && len(call.Args) >= s.MinArgs {
			return s, true
		}
	case *ast.Ident:
		if s, ok := r.shapes[fn.Name]; ok && len(call.Args) >= s.MinArgs {
			return s, true
		}
	}
	return callShape{}, false
}

// sitesOf renders every {method, path} a call can produce. A call with an
// unresolvable verb yields nothing rather than a guess.
func (r *pathResolver) sitesOf(call *ast.CallExpr, sc *scope) []struct{ Method, Path string } {
	shape, ok := r.shapeOf(call)
	if !ok {
		return nil
	}
	methods := []string{shape.Method}
	if shape.Method == "" {
		if shape.MethodArg >= len(call.Args) {
			return nil
		}
		methods = r.renderMethod(call.Args[shape.MethodArg], sc)
	}
	var out []struct{ Method, Path string }
	for _, m := range methods {
		for _, tmpl := range shape.Templates {
			for _, p := range r.substitute(tmpl, call.Args, sc) {
				out = append(out, struct{ Method, Path string }{m, p})
			}
		}
	}
	return out
}

// ─── building the resolver from source ───────────────────────────────────

// bodyScope collects every assignment to a plain identifier in a function
// body: `x := …`, `x = …`, and in-body `const`/`var` declarations. Nested
// function literals are included, so a closure can resolve a name its
// enclosing function built.
func bodyScope(body *ast.BlockStmt) *scope {
	sc := &scope{exprs: map[string][]ast.Expr{}}
	record := func(name string, e ast.Expr) {
		if name == "" || name == "_" || e == nil {
			return
		}
		sc.exprs[name] = append(sc.exprs[name], e)
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch st := n.(type) {
		case *ast.AssignStmt:
			// Only single-value assignments carry a renderable RHS;
			// `id, err := f()` leaves id unresolved, which renders `{}`.
			if len(st.Lhs) == 1 && len(st.Rhs) == 1 {
				if id, ok := st.Lhs[0].(*ast.Ident); ok {
					record(id.Name, st.Rhs[0])
				}
			}
		case *ast.DeclStmt:
			gd, ok := st.Decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						record(name.Name, vs.Values[i])
					}
				}
			}
		}
		return true
	})
	return sc
}

// newPathResolver reads both CLI packages and works out, by fixed point:
// package-level path consts, funcs that return a path, and forwarders — any
// func that hands one of its own params to a call shape. Three passes are
// enough for the tree as it stands (a forwarder of a forwarder of a wrapper);
// the loop runs until nothing new appears, so depth is not a magic number.
func newPathResolver(files []*ast.File) *pathResolver {
	r := &pathResolver{
		globals: map[string][]ast.Expr{},
		helpers: map[string][]string{},
		shapes:  map[string]callShape{},
	}
	for name, s := range wrapperShapes {
		r.shapes[name] = s
	}

	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i < len(vs.Values) && name.Name != "_" {
						r.globals[name.Name] = append(r.globals[name.Name], vs.Values[i])
					}
				}
			}
		}
	}

	// The loop below is a fixed point, but a bug in a future render rule
	// could keep it moving forever; bound it so the failure is a wrong
	// answer someone can debug rather than a hung test binary.
	for pass := 0; pass < 16; pass++ {
		changed := false
		for _, f := range files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Name == nil {
					continue
				}
				if r.learnPathHelper(fn) {
					changed = true
				}
				if r.learnForwarder(fn) {
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	return r
}

// learnPathHelper registers a func whose first result is a string and whose
// returns render to an /api/ path (gdprUserPath, chatStreamPath, …).
func (r *pathResolver) learnPathHelper(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
		return false
	}
	if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || id.Name != "string" {
		return false
	}
	sc := bodyScope(fn.Body)
	var found []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		for _, c := range r.render(ret.Results[0], sc, map[string]bool{"func:" + fn.Name.Name: true}) {
			if strings.HasPrefix(c, "/api/") {
				found = append(found, c)
			}
		}
		return true
	})
	if len(found) == 0 {
		return false
	}
	found = dedupCandidates(found)
	if sameStrings(r.helpers[fn.Name.Name], found) {
		return false
	}
	r.helpers[fn.Name.Name] = found
	return true
}

// learnForwarder registers a func that passes one of its own string params
// through to a known call shape — putBytes, postMultipart, cuidFetch,
// getByRef. The param stands in as `«argN»`, so the caller's argument is what
// finally fills the hole and the template survives a concatenation
// (`getByRef` builds singlePrefix + ref).
func (r *pathResolver) learnForwarder(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Name == nil {
		return false
	}
	// Wrappers we hard-code are authoritative; don't let discovery
	// re-derive (and possibly narrow) them.
	if _, fixed := wrapperShapes[fn.Name.Name]; fixed {
		return false
	}
	sc := bodyScope(fn.Body)
	sc.fixed = map[string][]string{}
	idx := 0
	for _, field := range fn.Type.Params.List {
		id, isString := field.Type.(*ast.Ident)
		for _, name := range field.Names {
			if isString && id.Name == "string" && name.Name != "_" {
				sc.fixed[name.Name] = []string{hole(idx)}
			}
			idx++
		}
		if len(field.Names) == 0 {
			idx++
		}
	}
	if len(sc.fixed) == 0 {
		return false
	}

	var method string
	var tmpls []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, s := range r.sitesOf(call, sc) {
			if !holeRE.MatchString(s.Path) {
				continue // not forwarding a param — an ordinary call site
			}
			if method != "" && method != s.Method {
				// Two different verbs off the same param: describing that
				// as one shape would invent call sites. Leave it alone.
				method = "?"
				return false
			}
			method = s.Method
			tmpls = append(tmpls, s.Path)
		}
		return true
	})
	if method == "" || method == "?" || len(tmpls) == 0 {
		return false
	}
	tmpls = dedupCandidates(tmpls)
	if prev, ok := r.shapes[fn.Name.Name]; ok && prev.Method == method && sameStrings(prev.Templates, tmpls) {
		return false
	}
	r.shapes[fn.Name.Name] = callShape{Method: method, Templates: tmpls, MinArgs: 1}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// collectCLICallSites walks the CLI packages and returns every API call the
// binary can make, tagged with whether its enclosing function cleared the
// workspace first.
func collectCLICallSites(t *testing.T) []cliCallSite {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()

	var files []*ast.File
	for _, dir := range []string{
		filepath.Join(root, "cmd", "crewship"),
		filepath.Join(root, "internal", "cli"),
	} {
		for _, path := range goFiles(t, dir) {
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			files = append(files, f)
		}
	}
	resolver := newPathResolver(files)

	var sites []cliCallSite
	for _, f := range files {
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
			sites = append(sites, callSitesInBody(fset, resolver, body)...)
			return true
		})
	}
	return sites
}

// callSitesInBody extracts the API calls in one function body. Nested function
// literals are visited by the outer walk too; the duplicate is harmless
// because both copies assert the same thing.
func callSitesInBody(fset *token.FileSet, resolver *pathResolver, body *ast.BlockStmt) []cliCallSite {
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

	sc := bodyScope(body)
	var out []cliCallSite
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		for _, s := range resolver.sitesOf(call, sc) {
			if !strings.HasPrefix(s.Path, "/api/") {
				continue
			}
			// A hole survives only inside a forwarder TEMPLATE, never at a
			// real call site (every index is filled, out-of-range ones with
			// `{}`). Belt and braces: reporting one would be an unreadable
			// path with a NUL in it rather than an honest finding.
			if holeRE.MatchString(s.Path) {
				continue
			}
			out = append(out, cliCallSite{
				Method:          s.Method,
				Path:            normalisePath(s.Path),
				Raw:             s.Path,
				Pos:             fset.Position(call.Pos()).String(),
				ClearsWorkspace: cleared,
			})
		}
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
	// postRoutineGovernance (cmd_routine_governance.go) picks `action` at
	// runtime; all four are registered — router_pipelines.go:69 (approve),
	// :70 (reject), :79 (enable), :80 (disable).
	"POST /api/v1/workspaces/{}/pipelines/{}/{}": "governance action is a runtime-chosen verb: approve|reject|enable|disable, all registered",
	// hooksToggle (cmd_hooks.go) builds `…/hooks/{id}/{enable|disable}` from
	// a bool; both halves are registered — router_orchestration.go:548,549.
	"POST /api/v1/hooks/{}/{}": "toggle verb is runtime-chosen: enable|disable, both registered",
}

// ─── invariant 1: every CLI call reaches a route that exists ─────────────

// minCallSites is the vacuity floor: below this the extractor has stopped
// matching and both invariants would pass without checking anything. It sat
// at 100 against ~450 extracted sites, which is not a guard so much as a
// rumour of one — closing the three gaps in #2086 took the real number past
// 950, so the floor moves with it. Lower it only alongside a real, explained
// shrink of the CLI's API surface.
const minCallSites = 750

func TestCLICallsHitRegisteredRoutes(t *testing.T) {
	routes := collectAPIRoutes(t)
	sites := collectCLICallSites(t)
	if len(sites) < minCallSites {
		t.Fatalf("CLI call-site extractor found only %d call sites (floor %d) — it stopped matching, "+
			"so this invariant would pass without checking anything", len(sites), minCallSites)
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
		t.Errorf("%s calls %s %s (path expression resolves to %q) but the router does not register it — %s",
			s.Pos, s.Method, s.Path, s.Raw, hint)
	}
}

// ─── invariant 2: don't clear the workspace on a workspace-gated route ───

// knownWorkspaceClearingDrift are violations already being fixed on another
// branch. An entry here is a promise, not an excuse: the test fails if one
// stops matching, so a merged fix forces the entry out instead of leaving a
// permanent hole. Never add an entry for a bug nobody is fixing.
var knownWorkspaceClearingDrift = map[string]string{}

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
