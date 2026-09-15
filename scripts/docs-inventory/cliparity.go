package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CLI parity is the rule "every API endpoint gets a CLI command", measured.
//
// Until this file existed nothing measured it. The 2026-09-15 audit found five
// of the ninety-three September routes with no caller anywhere in cmd/crewship
// — calendar, executions, artifacts, the memory inventories, panel history —
// each one documented, each one in the spec, each one green on every gate this
// tool ran. The gates proved the route was described; none of them asked
// whether the contract agents actually use could reach it.
//
// The check is a diff of two sets. One side is every public path the OpenAPI
// document lists. The other is every request path the CLI can build, read out
// of the Go source of cmd/crewship rather than out of a list someone maintains:
// a string literal, a `+` chain of literals and variables, an fmt.Sprintf
// format, a helper that returns one of those. Each becomes a SHAPE — the path
// with every non-literal part replaced by a placeholder that matches exactly
// one segment — and a route is covered when some shape matches it segment for
// segment. Methods are not compared: a path the CLI reaches for GET but not for
// DELETE would need the request wrappers (getJSON, client.Post, chatRoomRequest
// with a method argument) traced too, and a path-level diff already catches the
// failure the audit found, which was a path nobody called at all.
//
// A route can be exempted, with a reason, in cliParityExemptionsPath. The file
// is the record the audit asked for: the one endpoint that serves iframe HTML,
// the public dispatch routes a CLI cannot usefully drive, and — marked as such
// — routes whose CLI is in flight on another branch.

const (
	cliParityExemptionsPath = "scripts/docs-inventory/cli-parity-exemptions.txt"
	// cliPathPlaceholder marks the non-literal part of a path expression while
	// the shape is being built. It is not a byte that can occur in a Go string
	// used as a URL path.
	cliPathPlaceholder = "\x00"
)

// cliSourceRoots are where the CLI builds request paths: the commands, and the
// client library they call for the paths that are wrapped in a method
// (`client.CrewCapabilities(ctx, id)`). internal/cli/clitest is the stub
// server the CLI's unit tests talk to; a path it knows is not a path the CLI
// can send.
var cliSourceRoots = []string{"cmd/crewship/", "internal/cli/"}

const cliTestHelperRoot = "internal/cli/clitest/"

func isCLISourceFile(path string) bool {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, cliTestHelperRoot) {
		return false
	}
	for _, root := range cliSourceRoots {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
}

// cliParityStatus values carried on apiRecord.CLIParity.
const (
	cliParityCovered = "cli"
	cliParityExempt  = "exempt"
	cliParityMissing = "missing"
)

var sprintfVerb = regexp.MustCompile(`%[-+# 0-9.*\[\]]*[a-zA-Z]`)

// cliPathShape is one request path the CLI source can build, and where.
type cliPathShape struct {
	Shape  string
	Source string
}

// cliPathShapes reads every non-test Go file under cliSourceRoots out of
// sources and returns the request-path shapes it can build.
func cliPathShapes(sources []docFile) ([]cliPathShape, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	var paths []string
	for _, source := range sources {
		if !isCLISourceFile(source.Path) {
			continue
		}
		file, err := parser.ParseFile(fset, source.Path, source.Text, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", source.Path, err)
		}
		files = append(files, file)
		paths = append(paths, source.Path)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go source under %s — the CLI moved, so this gate has nothing to compare routes against", strings.Join(cliSourceRoots, ", "))
	}

	pkg := newShapeEnv(nil)
	// Pass one: package-level names. Constants and variables holding a path
	// prefix (`const chatRoomBase = "/api/v1/conversations"`) and helpers that
	// return one (`func chatRoomPath(id string) string { return chatRoomBase +
	// "/" + url.PathEscape(id) }`) are how the CLI avoids repeating prefixes,
	// and a caller that writes `chatRoomPath(id) + "/messages"` only resolves
	// to a route once those are known.
	for _, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range value.Names {
						if i < len(value.Values) {
							pkg.bind(name.Name, value.Values[i])
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Body != nil {
					pkg.bindFunc(d)
				}
			}
		}
	}

	seen := map[string]bool{}
	var shapes []cliPathShape
	for i, file := range files {
		for _, decl := range file.Decls {
			// Pass two, per top-level declaration: local assignments first, then
			// every expression in it rendered against local + package names.
			// Declarations rather than functions because most of the CLI is
			// `var xCmd = &cobra.Command{RunE: func(...) error {...}}` — the
			// request is built inside a function literal in a var block.
			local := newShapeEnv(pkg)
			local.bindAssignments(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				expr, ok := n.(ast.Expr)
				if !ok {
					return true
				}
				for _, shape := range local.shapes(expr, 0) {
					shape = normalizeShape(shape)
					if shape == "" {
						continue
					}
					key := shape + "\n" + paths[i]
					if !seen[key] {
						seen[key] = true
						shapes = append(shapes, cliPathShape{Shape: shape, Source: paths[i]})
					}
				}
				return true
			})
		}
	}
	sort.Slice(shapes, func(i, j int) bool {
		if shapes[i].Shape != shapes[j].Shape {
			return shapes[i].Shape < shapes[j].Shape
		}
		return shapes[i].Source < shapes[j].Source
	})
	return shapes, nil
}

// shapeEnv resolves identifiers to the string expressions bound to them. A
// name may be bound more than once — `path` is reassigned in half the CLI's
// list commands — and every binding is kept, because a gate that picked one
// would silently drop the routes the others build.
type shapeEnv struct {
	parent *shapeEnv
	names  map[string][]ast.Expr
	// rendered holds names already reduced to shapes: a helper's parameters,
	// bound to the caller's arguments at the call site.
	rendered map[string][]string
	funcs    map[string]*funcShape
}

// funcShape is a package-level function as a path builder: what it returns,
// under which parameter names, with its own local assignments in scope.
type funcShape struct {
	params  []string
	returns []ast.Expr
	local   *shapeEnv
}

func newShapeEnv(parent *shapeEnv) *shapeEnv {
	return &shapeEnv{parent: parent, names: map[string][]ast.Expr{}, rendered: map[string][]string{}, funcs: map[string]*funcShape{}}
}

func (e *shapeEnv) bind(name string, value ast.Expr) {
	if name == "_" {
		return
	}
	e.names[name] = append(e.names[name], value)
}

// bindAssignments records every `x := v`, `x = v` and `x += v` under node.
// The compound form matters: `endpoint += "/source"` is how the page project
// command reaches four routes from one base, and it is bound as
// `endpoint + "/source"` so the earlier bindings of endpoint feed into it.
func (e *shapeEnv) bindAssignments(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for j, lhs := range assign.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			switch assign.Tok {
			case token.ADD_ASSIGN:
				e.bind(ident.Name, &ast.BinaryExpr{X: ident, Op: token.ADD, Y: assign.Rhs[j]})
			case token.ASSIGN, token.DEFINE:
				e.bind(ident.Name, assign.Rhs[j])
			}
		}
		return true
	})
}

// bindFunc records a package-level function as a builder. Its parameters are
// bound at each call site, so `workspacePath(client, "/work-items/"+id+
// "/cancel")` renders with the caller's suffix rather than a one-segment
// placeholder — which is what `/work-items/{}/cancel` needs to be reached.
func (e *shapeEnv) bindFunc(fn *ast.FuncDecl) {
	shape := &funcShape{local: newShapeEnv(e)}
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			for _, name := range field.Names {
				shape.params = append(shape.params, name.Name)
			}
		}
	}
	shape.local.bindAssignments(fn.Body)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if ok && len(ret.Results) >= 1 {
			shape.returns = append(shape.returns, ret.Results[0])
		}
		return true
	})
	if len(shape.returns) > 0 {
		e.funcs[fn.Name.Name] = shape
	}
}

func (e *shapeEnv) lookup(name string) ([]ast.Expr, []string) {
	for env := e; env != nil; env = env.parent {
		if values, ok := env.rendered[name]; ok {
			return nil, values
		}
		if values, ok := env.names[name]; ok {
			return values, nil
		}
	}
	return nil, nil
}

func (e *shapeEnv) lookupFunc(name string) *funcShape {
	for env := e; env != nil; env = env.parent {
		if fn, ok := env.funcs[name]; ok {
			return fn
		}
	}
	return nil
}

// maxShapeDepth bounds identifier resolution: `path = path + "/x"` would
// otherwise recurse forever, and a chain deeper than this is not how request
// paths are written.
const maxShapeDepth = 4

// shapes renders every string an expression can evaluate to, with each part
// this tool cannot read replaced by cliPathPlaceholder. A literal has one
// rendering; an identifier bound twice has two; a `+` chain has the product.
func (e *shapeEnv) shapes(expr ast.Expr, depth int) []string {
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return []string{cliPathPlaceholder}
		}
		value, err := strconv.Unquote(x.Value)
		if err != nil {
			return []string{cliPathPlaceholder}
		}
		return []string{value}
	case *ast.ParenExpr:
		return e.shapes(x.X, depth)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return []string{cliPathPlaceholder}
		}
		var out []string
		for _, left := range e.shapes(x.X, depth) {
			for _, right := range e.shapes(x.Y, depth) {
				out = append(out, left+right)
			}
		}
		return out
	case *ast.Ident:
		if depth >= maxShapeDepth {
			return []string{cliPathPlaceholder}
		}
		values, rendered := e.lookup(x.Name)
		if len(rendered) > 0 {
			return rendered
		}
		if len(values) == 0 {
			return []string{cliPathPlaceholder}
		}
		var out []string
		for _, value := range values {
			out = append(out, e.shapes(value, depth+1)...)
		}
		return out
	case *ast.CallExpr:
		return e.callShapes(x, depth)
	}
	return []string{cliPathPlaceholder}
}

func (e *shapeEnv) callShapes(call *ast.CallExpr, depth int) []string {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		if ok && pkg.Name == "fmt" && strings.HasPrefix(fn.Sel.Name, "Sprint") && len(call.Args) > 0 {
			if fn.Sel.Name != "Sprintf" {
				return []string{cliPathPlaceholder}
			}
			var out []string
			for _, format := range e.shapes(call.Args[0], depth) {
				out = append(out, strings.ReplaceAll(sprintfVerb.ReplaceAllString(format, cliPathPlaceholder), "%%", "%"))
			}
			return out
		}
	case *ast.Ident:
		if depth >= maxShapeDepth {
			return []string{cliPathPlaceholder}
		}
		if helper := e.lookupFunc(fn.Name); helper != nil {
			callee := newShapeEnv(helper.local)
			for i, param := range helper.params {
				if i < len(call.Args) {
					callee.rendered[param] = e.shapes(call.Args[i], depth+1)
				}
			}
			var out []string
			for _, ret := range helper.returns {
				out = append(out, callee.shapes(ret, depth+1)...)
			}
			return out
		}
	}
	return []string{cliPathPlaceholder}
}

// normalizeShape reduces a rendered string to a comparable path shape, or to
// "" when it is not a request path at all. The query string is dropped —
// routes do not carry one — and a segment that is nothing but a placeholder
// becomes the one-segment wildcard `{}`, the same spelling normalizeRoutePath
// gives a route's `{crewId}`. A segment that is a literal followed by a
// placeholder — `fmt.Sprintf("/api/v1/chains%s", qs)`, where qs is a query
// string or nothing — keeps the literal and a trailing `*`: it reaches the
// segments that begin with that literal, and not, as a bare wildcard would,
// every top-level resource in the API.
//
// Only shapes rooted at /api/ are kept. A shape that begins with a placeholder
// is a path built on a base this tool could not resolve; matching it as a
// suffix would let `base + "/cancel"` claim every route ending in /cancel, so
// it is dropped and the base has to be readable instead.
func normalizeShape(shape string) string {
	if i := strings.IndexByte(shape, '?'); i >= 0 {
		shape = shape[:i]
	}
	if !strings.HasPrefix(shape, "/api/") {
		return ""
	}
	parts := strings.Split(strings.Trim(shape, "/"), "/")
	for i, part := range parts {
		at := strings.Index(part, cliPathPlaceholder)
		switch {
		case at < 0:
		case at == 0:
			parts[i] = "{}"
		default:
			parts[i] = part[:at] + "*"
		}
	}
	return strings.Join(parts, "/")
}

// shapeMatchesRoute reports whether a normalized shape reaches a normalized
// route: same segment count, and every segment either equal or a wildcard on
// the shape's side. A literal in the shape may stand for a parameter in the
// route (`/api/v1/workspaces/current` reaches `/api/v1/workspaces/{id}`); a
// literal in the route is never satisfied by a different literal.
//
// routes is every normalized path in the spec, and it settles the one
// ambiguous case: a wildcard against a route literal. `routine get <slug>`
// builds `/pipelines/{}`, and the spec has both `/pipelines/{slug}` and
// `/pipelines/calendar`. The wildcard reaches the parameter route; it does
// not reach the calendar, because a caller that passes "calendar" as a slug
// is not a calendar command — and that route was one of the five the audit
// found with no CLI. So a wildcard claims a literal only when no sibling
// route takes a parameter at that position.
func shapeMatchesRoute(shape, route string, routes map[string]bool) bool {
	want := strings.Split(route, "/")
	got := strings.Split(shape, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		switch {
		case got[i] == want[i], want[i] == "{}":
			continue
		case strings.HasSuffix(got[i], "*"):
			if strings.HasPrefix(want[i], strings.TrimSuffix(got[i], "*")) {
				continue
			}
		case got[i] == "{}":
			sibling := append(append([]string{}, want[:i]...), "{}")
			sibling = append(sibling, want[i+1:]...)
			if routes[strings.Join(sibling, "/")] {
				return false
			}
			continue
		}
		return false
	}
	return true
}

// cliCallers returns the CLI source files whose path shapes reach route.
func cliCallers(route string, shapes []cliPathShape, routes map[string]bool) []string {
	normalized := normalizeRoutePath(route)
	var out []string
	for _, shape := range shapes {
		if shapeMatchesRoute(shape.Shape, normalized, routes) {
			out = appendUnique(out, shape.Source)
		}
	}
	return limitSignals(out)
}

// specRoutes is the normalized path set of the OpenAPI document, the sibling
// lookup shapeMatchesRoute needs.
func specRoutes(doc openAPIDocument) map[string]bool {
	routes := make(map[string]bool, len(doc.Paths))
	for route := range doc.Paths {
		routes[normalizeRoutePath(route)] = true
	}
	return routes
}

// cliParityExemption is one route the gate does not hold to parity, and why.
type cliParityExemption struct {
	Path   string
	Reason string
	Line   int
}

// readCLIParityExemptions parses the exemption file: one route per line,
// followed by whitespace and the reason; `#` lines and blank lines are
// skipped. A route with no reason is an error, because an exemption nobody
// can justify is the gate quietly shrinking.
func readCLIParityExemptions(name string) ([]cliParityExemption, error) {
	file, err := os.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	defer file.Close()
	var out []cliParityExemption
	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path, reason, _ := strings.Cut(line, " ")
		path, tabReason, tab := strings.Cut(path, "\t")
		if tab {
			reason = tabReason + " " + reason
		}
		reason = strings.TrimSpace(reason)
		if !strings.HasPrefix(path, "/api/") || reason == "" {
			return nil, fmt.Errorf("%s:%d: expected `/api/... <reason>`, got %q", name, lineNo, line)
		}
		out = append(out, cliParityExemption{Path: path, Reason: reason, Line: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return out, nil
}

// cliParityFor decides one route's parity status: covered when a CLI shape
// reaches it, exempt when the file says so, missing otherwise. The exemption
// is consulted second so that a route which gained a CLI keeps reporting
// "cli" — the stale exemption is then listed by staleCLIParityExemptions.
func cliParityFor(path string, callers []string, exemptions []cliParityExemption) string {
	if len(callers) > 0 {
		return cliParityCovered
	}
	for _, ex := range exemptions {
		if normalizeRoutePath(ex.Path) == normalizeRoutePath(path) {
			return cliParityExempt
		}
	}
	return cliParityMissing
}

// staleCLIParityExemptions names exemptions the tree no longer needs: the route
// has a CLI caller now, or is no longer in the spec. They are reported rather
// than failed — the branch that adds the CLI should not have to edit this file
// in the same commit — but every one of them is a line that should go.
func staleCLIParityExemptions(exemptions []cliParityExemption, records []apiRecord) []string {
	status := map[string]string{}
	for _, rec := range records {
		key := normalizeRoutePath(rec.Path)
		if status[key] != cliParityCovered {
			status[key] = rec.CLIParity
		}
	}
	var out []string
	for _, ex := range exemptions {
		switch status[normalizeRoutePath(ex.Path)] {
		case cliParityCovered:
			out = append(out, fmt.Sprintf("%s:%d: %s now has a CLI caller — remove the exemption", cliParityExemptionsPath, ex.Line, ex.Path))
		case "":
			out = append(out, fmt.Sprintf("%s:%d: %s is not in the OpenAPI document — remove the exemption", cliParityExemptionsPath, ex.Line, ex.Path))
		}
	}
	return out
}
