package main

import (
	"bufio"
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
	"unicode"
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
// The check is a diff of two sets. One side is every public operation — method
// and path — the OpenAPI document lists. The other is every request the CLI
// can send, read out of the Go source of cmd/crewship rather than out of a
// list someone maintains.
//
// The path of a request is read from a string literal, a `+` chain of
// literals and variables, an fmt.Sprintf format, a helper that returns one of
// those (or one result of a helper that returns several), a local or package
// constant. Each becomes a SHAPE — the path with every non-literal part
// replaced by a placeholder that matches exactly one segment — and a route is
// reached when some shape matches it segment for segment. A shape is only
// collected from the argument position of a call that actually sends a
// request — the client methods (Get/Post/Do/…) and any wrapper, package-level
// or closure, discovered to hand one of its parameters into that position — so
// a "/api/…" string in help text, a log message or a comparison cannot claim a
// route no request ever names.
//
// The method of each such call is read the same way, through the same
// layers: the client method's name (Get, Patch), the method argument of Do
// and NewRequest (a literal, an http.MethodX constant, a local bound to
// either, a map or slice literal indexed or ranged over), a wrapper that
// passes its own method parameter on (chatRoomRequest, teamSeedJSON), a
// wrapper that dispatches on it (`if method == "GET" { Get } else { Post }`,
// which sends only what it compares against), a callback (the union of the
// function literals passed in), and a request function whose method arrives
// from its callers (sharedProjectMilestoneRequest). Until 2026-10-29 only
// paths were compared, so a path the CLI reached for DELETE counted for PATCH
// too; fifteen operations were unreachable that way.
//
// Where the method cannot be read statically the call is METHOD-UNKNOWN. It
// still reaches its path for every method — the path-level rule this gate
// had before — so the gate never reports a gap it cannot prove, but nothing
// is guessed either: those operations are flagged on the record, and the
// summary line counts both them and the call sites behind them. A method set
// and a path set read from the same call are combined as a product, so a call
// whose method depends on whether an id was given (POST /milestones, PUT
// /milestones/{id}) also claims the cross terms; that imprecision is the
// same one a path variable bound twice already had.
//
// `crewship seed` provisions demo data through the API, and a route only seed
// calls is not one a user's command reaches. A declaration in cmd_seed*.go
// therefore contributes shapes only when a declaration outside those files
// reaches it through a function or method call: the nuke commands' helpers
// live in cmd_seed_data_nuke.go and keep counting, the seeders do not.
//
// An operation can be exempted, with a reason, in cliParityExemptionsPath —
// either one method (`PUT /api/v1/crews/{crewId}  reason`) or, with no method,
// every method on the path. The file is the record the audit asked for: the
// one endpoint that serves iframe HTML, the public dispatch routes a CLI
// cannot usefully drive, and — marked as such — operations whose CLI is in
// flight on another branch.

const (
	cliParityExemptionsPath = "scripts/docs-inventory/cli-parity-exemptions.txt"
	// cliPathPlaceholder marks the non-literal part of a path expression while
	// the shape is being built. It is not a byte that can occur in a Go string
	// used as a URL path.
	cliPathPlaceholder = "\x00"
	// cliResultMarker names the synthetic call that stands for one result of
	// a multi-value call (`client, base, err := guestIssueClient()` binds base
	// to result 1). No Go identifier can contain the byte.
	cliResultMarker = "\x00result"
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

// isCLISeedFile names the files of `crewship seed`, whose requests count only
// where a user-facing command reaches them (see the file comment).
func isCLISeedFile(path string) bool {
	return strings.HasPrefix(path, "cmd/crewship/cmd_seed")
}

// cliParityStatus values carried on apiRecord.CLIParity.
const (
	cliParityCovered = "cli"
	cliParityExempt  = "exempt"
	cliParityMissing = "missing"
)

var sprintfVerb = regexp.MustCompile(`%[-+# 0-9.*\[\]]*[a-zA-Z]`)

// httpMethods are the methods an operation or an exemption can name.
var httpMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "OPTIONS": true, "TRACE": true, "CONNECT": true,
}

// cliPathShape is one request path the CLI source can build, where, and with
// which methods.
type cliPathShape struct {
	Shape  string
	Source string
	// Methods are the methods the calls building this shape in Source send.
	Methods []string
	// MethodUnknown is set when at least one of those calls sends a method
	// this tool cannot read; the shape then reaches its path for every
	// method. UnknownSites are those calls, as file:line.
	MethodUnknown bool
	UnknownSites  []string
}

// sinkSpec says how a call sends a request: which argument carries the path,
// and where the method comes from.
type sinkSpec struct {
	path int
	// methods are sent whatever the arguments.
	methods []string
	// methodArg is the argument carrying the method, or -1.
	methodArg int
	// dispatch, when not nil, means the callee compares its method argument
	// against these and sends one of them; an argument outside the set is
	// sent as something this tool does not follow, so it is unknown.
	dispatch []string
	// unknown: the call can send a method this tool cannot read.
	unknown bool
}

func (s sinkSpec) equal(o sinkSpec) bool {
	return s.path == o.path && s.methodArg == o.methodArg && s.unknown == o.unknown &&
		strings.Join(s.methods, ",") == strings.Join(o.methods, ",") &&
		(s.dispatch == nil) == (o.dispatch == nil) && strings.Join(s.dispatch, ",") == strings.Join(o.dispatch, ",")
}

// cliSinkMethods are the client methods that send an HTTP request. Matched by
// method name on any receiver: a getter with one of these names whose
// argument is a query key or a map key produces no shape, because such an
// argument never starts with /api/. Do is `Do(method, path, body)`;
// NewRequest is `NewRequest(ctx, method, path, body)`. http.Client.Do(req)
// has one argument and so no path position at all.
var cliSinkMethods = map[string]sinkSpec{
	"Get":        {path: 0, methods: []string{"GET"}, methodArg: -1},
	"Post":       {path: 0, methods: []string{"POST"}, methodArg: -1},
	"Patch":      {path: 0, methods: []string{"PATCH"}, methodArg: -1},
	"Put":        {path: 0, methods: []string{"PUT"}, methodArg: -1},
	"Delete":     {path: 0, methods: []string{"DELETE"}, methodArg: -1},
	"Do":         {path: 1, methodArg: 0},
	"NewRequest": {path: 2, methodArg: 1},
}

// sinkSet names the calls a shape may be collected from: client methods
// directly, plus wrappers — functions, methods and closures discovered to
// pass one of their parameters into the path position of such a call — as
// name → spec. A set is built per package (a name in internal/cli must not
// match a call in cmd/crewship) and copied per declaration so a closure's
// name stays scoped to its own declaration.
type sinkSet struct {
	methods map[string]sinkSpec
	funcs   map[string]sinkSpec
}

// forDecl returns an independent copy, safe for per-declaration additions.
// The method map is shared: method sinks bind wherever their receiver goes.
func (s *sinkSet) forDecl() *sinkSet {
	funcs := make(map[string]sinkSpec, len(s.funcs))
	for k, v := range s.funcs {
		funcs[k] = v
	}
	return &sinkSet{methods: s.methods, funcs: funcs}
}

func cloneIntMap(src map[string]int) map[string]int {
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// spec reports how call sends a request, if it does.
func (s *sinkSet) spec(call *ast.CallExpr) (sinkSpec, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		spec, ok := s.methods[fn.Sel.Name]
		return spec, ok
	case *ast.Ident:
		spec, ok := s.funcs[fn.Name]
		return spec, ok
	}
	return sinkSpec{}, false
}

// methodSet is what a method expression, or a call, can send.
type methodSet struct {
	methods []string
	// param is the parameter of the enclosing wrapper the method comes
	// from, or -1; dispatch is carried with it (see sinkSpec).
	param    int
	dispatch []string
	unknown  bool
}

func (m *methodSet) add(methods ...string) {
	for _, method := range methods {
		m.methods = appendUnique(m.methods, method)
	}
	sort.Strings(m.methods)
}

func (m *methodSet) merge(o methodSet) {
	m.add(o.methods...)
	m.unknown = m.unknown || o.unknown
	if o.param >= 0 && (m.param < 0 || o.param < m.param) {
		m.param = o.param
	}
	if o.dispatch != nil {
		m.dispatch = appendUnique(m.dispatch, o.dispatch...)
		sort.Strings(m.dispatch)
	}
}

// cliPackage is one Go package of CLI source, with what the method reader
// needs to look across declarations: who calls a function, and each
// declaration's own bindings.
type cliPackage struct {
	env     *shapeEnv
	sinks   *sinkSet
	files   []*ast.File
	callers map[string][]cliCallSite
	envs    map[ast.Decl]*shapeEnv
}

type cliCallSite struct {
	call *ast.CallExpr
	decl ast.Decl
}

// declEnv is decl's local bindings over the package's.
func (p *cliPackage) declEnv(decl ast.Decl) *shapeEnv {
	if env, ok := p.envs[decl]; ok {
		return env
	}
	env := newShapeEnv(p.env)
	env.bindAssignments(decl)
	p.envs[decl] = env
	return env
}

// methodScope resolves identifiers in a method expression: params are the
// wrapper parameters being discovered (nil outside discovery), fn the
// enclosing top-level function whose callers can supply a parameter.
type methodScope struct {
	env    *shapeEnv
	params map[string]int
	fn     *ast.FuncDecl
	pkg    *cliPackage
	depth  int
}

// maxMethodDepth bounds method resolution through bindings and callers.
const maxMethodDepth = 4

func unknownMethod() methodSet { return methodSet{param: -1, unknown: true} }

// evalMethod reads the methods expr can evaluate to.
func evalMethod(expr ast.Expr, scope methodScope) methodSet {
	out := methodSet{param: -1}
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if value, err := strconv.Unquote(x.Value); err == nil && httpMethods[strings.ToUpper(value)] {
				out.add(strings.ToUpper(value))
				return out
			}
		}
		return unknownMethod()
	case *ast.ParenExpr:
		return evalMethod(x.X, scope)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "http" && strings.HasPrefix(x.Sel.Name, "Method") {
			if method := strings.ToUpper(strings.TrimPrefix(x.Sel.Name, "Method")); httpMethods[method] {
				out.add(method)
				return out
			}
		}
		return unknownMethod()
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ToUpper" && len(x.Args) == 1 {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" {
				return evalMethod(x.Args[0], scope)
			}
		}
		return unknownMethod()
	case *ast.IndexExpr:
		return evalMethodElements(x.X, scope)
	case *ast.Ident:
		if scope.depth >= maxMethodDepth {
			return unknownMethod()
		}
		resolved := false
		if pi, ok := scope.params[x.Name]; ok {
			out.param = pi
			resolved = true
		}
		next := scope
		next.depth++
		if scope.env != nil {
			values, _ := scope.env.lookup(x.Name)
			for _, value := range values {
				if id, ok := value.(*ast.Ident); ok && id.Name == x.Name {
					continue
				}
				out.merge(evalMethod(value, next))
				resolved = true
			}
		}
		// A parameter of the enclosing function that is not a wrapper being
		// discovered: its callers supply it, alongside any value the body
		// assigns over it (`if method == "" { method = "GET" }`).
		if scope.params == nil && scope.fn != nil && scope.pkg != nil {
			if pi, ok := paramIndex(scope.fn.Type.Params)[x.Name]; ok {
				out.merge(methodFromCallers(scope.fn.Name.Name, pi, scope))
				resolved = true
			}
		}
		if !resolved {
			return unknownMethod()
		}
		return out
	}
	return unknownMethod()
}

// evalMethodElements reads every element of a map or slice literal — what an
// index into it, or a range over it, can yield.
func evalMethodElements(expr ast.Expr, scope methodScope) methodSet {
	switch x := expr.(type) {
	case *ast.CompositeLit:
		if len(x.Elts) == 0 {
			return unknownMethod()
		}
		out := methodSet{param: -1}
		for _, elt := range x.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				elt = kv.Value
			}
			out.merge(evalMethod(elt, scope))
		}
		return out
	case *ast.Ident:
		if scope.env == nil || scope.depth >= maxMethodDepth {
			return unknownMethod()
		}
		values, _ := scope.env.lookup(x.Name)
		if len(values) == 0 {
			return unknownMethod()
		}
		next := scope
		next.depth++
		out := methodSet{param: -1}
		for _, value := range values {
			out.merge(evalMethodElements(value, next))
		}
		return out
	}
	return unknownMethod()
}

// methodFromCallers resolves a function's method parameter through every
// call to it in the package: sharedProjectMilestoneRequest builds its own
// path, and only its callers know which method it sends.
func methodFromCallers(name string, param int, scope methodScope) methodSet {
	sites := scope.pkg.callers[name]
	if len(sites) == 0 {
		return unknownMethod()
	}
	out := methodSet{param: -1}
	for _, site := range sites {
		if param >= len(site.call.Args) {
			out.unknown = true
			continue
		}
		caller := methodScope{env: scope.pkg.declEnv(site.decl), pkg: scope.pkg, depth: scope.depth + 1}
		if fn, ok := site.decl.(*ast.FuncDecl); ok {
			caller.fn = fn
		}
		got := evalMethod(site.call.Args[param], caller)
		got.param = -1
		out.merge(got)
	}
	return out
}

// callMethods reads what one request call sends, given its spec.
func callMethods(call *ast.CallExpr, spec sinkSpec, scope methodScope) methodSet {
	out := methodSet{param: -1, unknown: spec.unknown}
	out.add(spec.methods...)
	if spec.methodArg < 0 {
		return out
	}
	if spec.methodArg >= len(call.Args) {
		out.unknown = true
		return out
	}
	got := evalMethod(call.Args[spec.methodArg], scope)
	out.unknown = out.unknown || got.unknown
	if got.param >= 0 {
		out.param = got.param
		if spec.dispatch != nil {
			out.dispatch = append([]string{}, spec.dispatch...)
		}
	}
	for _, method := range got.methods {
		if spec.dispatch != nil && !contains(spec.dispatch, method) {
			out.unknown = true
			continue
		}
		out.add(method)
	}
	return out
}

// discoverDeclSinks adds closure wrappers (`fetch := func(path string)
// {…}`) to the declaration's own sink set, to a fixpoint so a closure
// calling an earlier-discovered closure is found too.
func discoverDeclSinks(decl ast.Node, sinks *sinkSet, scope methodScope) {
	for range 8 {
		changed := false
		ast.Inspect(decl, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != len(assign.Rhs) {
				return true
			}
			for j, lhs := range assign.Lhs {
				name, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				lit, ok := assign.Rhs[j].(*ast.FuncLit)
				if !ok || lit.Body == nil {
					continue
				}
				spec, ok := bodySinkSpec(lit.Body, paramIndex(lit.Type.Params), sinks, scope)
				if !ok {
					continue
				}
				if old, dup := sinks.funcs[name.Name]; !dup || !old.equal(spec) {
					sinks.funcs[name.Name] = spec
					changed = true
				}
			}
			return true
		})
		if !changed {
			return
		}
	}
}

// callbackRequestSinks finds request-forwarding function literals passed to a
// named function's function-typed parameter. The callback name is a sink only
// within that receiving declaration: another function's `call` may just log.
// Its methods are the union of every literal passed in — runCrewBudget's
// `call` is Get from one command, Put and Delete from the others.
func callbackRequestSinks(pkg *cliPackage) map[*ast.FuncDecl]map[string]sinkSpec {
	functions := map[string]*ast.FuncDecl{}
	for _, file := range pkg.files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				functions[fn.Name.Name] = fn
			}
		}
	}
	out := map[*ast.FuncDecl]map[string]sinkSpec{}
	for _, file := range pkg.files {
		for _, decl := range file.Decls {
			scope := methodScope{env: pkg.declEnv(decl), pkg: pkg}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				fn := functions[name.Name]
				if fn == nil || fn.Type.Params == nil {
					return true
				}
				position := 0
				for _, field := range fn.Type.Params.List {
					for _, param := range field.Names {
						if _, callable := field.Type.(*ast.FuncType); callable && position < len(call.Args) {
							if callback, ok := call.Args[position].(*ast.FuncLit); ok {
								if spec, ok := bodySinkSpec(callback.Body, paramIndex(callback.Type.Params), pkg.sinks, scope); ok {
									if out[fn] == nil {
										out[fn] = map[string]sinkSpec{}
									}
									if old, seen := out[fn][param.Name]; seen {
										spec = unionSpecs(old, spec)
									}
									out[fn][param.Name] = spec
								}
							}
						}
						position++
					}
					if len(field.Names) == 0 {
						position++
					}
				}
				return true
			})
		}
	}
	return out
}

func unionSpecs(a, b sinkSpec) sinkSpec {
	out := a
	if b.path < out.path {
		out.path = b.path
	}
	out.methods = appendUnique(append([]string{}, a.methods...), b.methods...)
	sort.Strings(out.methods)
	if out.methodArg < 0 {
		out.methodArg = b.methodArg
	}
	if b.dispatch != nil {
		out.dispatch = appendUnique(append([]string{}, a.dispatch...), b.dispatch...)
		sort.Strings(out.dispatch)
	}
	out.unknown = a.unknown || b.unknown
	return out
}

// bodySinkSpec walks a function body and reports whether one of its
// parameters reaches a request call's path argument — the lowest one,
// because a wrapper with two path-passing parameters would be ambiguous and
// the CLI does not write those — and what those calls send. A parameter
// reaches the path position when it is passed there directly
// (`client.Get(path)`) or through a plain local alias (`p := path;
// client.Get(p)`); anything more convoluted is not how these wrappers are
// written.
//
// The method is the union over those calls. A call whose method is one of
// the wrapper's own parameters makes that parameter the wrapper's method
// argument. A wrapper that sends fixed methods but compares a parameter
// against a method (`if method == "GET"`) dispatches on it: its callers'
// argument decides, within the methods it can send.
func bodySinkSpec(body ast.Node, params map[string]int, sinks *sinkSet, scope methodScope) (sinkSpec, bool) {
	if len(params) == 0 {
		return sinkSpec{}, false
	}
	names := cloneIntMap(params)
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for j, lhs := range assign.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			src, ok := assign.Rhs[j].(*ast.Ident)
			if !ok {
				continue
			}
			if pi, ok := names[src.Name]; ok {
				names[id.Name] = pi
			}
		}
		return true
	})
	local := scope
	local.params = names
	local.fn = nil
	if scope.env != nil {
		local.env = newShapeEnv(scope.env)
		local.env.bindAssignments(body)
	}
	found := -1
	methods := methodSet{param: -1}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		spec, ok := sinks.spec(call)
		if !ok || spec.path >= len(call.Args) {
			return true
		}
		id, ok := call.Args[spec.path].(*ast.Ident)
		if !ok {
			return true
		}
		pi, ok := names[id.Name]
		if !ok {
			return true
		}
		if found < 0 || pi < found {
			found = pi
		}
		methods.merge(callMethods(call, spec, local))
		return true
	})
	if found < 0 {
		return sinkSpec{}, false
	}
	out := sinkSpec{path: found, methods: methods.methods, methodArg: methods.param, dispatch: methods.dispatch, unknown: methods.unknown}
	if out.methodArg < 0 && !out.unknown && len(out.methods) > 0 {
		if pi, ok := dispatchParam(body, names, out.path, local); ok {
			out.methodArg, out.dispatch, out.methods = pi, out.methods, nil
		}
	}
	return out, true
}

// dispatchParam finds a parameter, other than the path, that the body
// compares with == or != against a method.
func dispatchParam(body ast.Node, names map[string]int, pathParam int, scope methodScope) (int, bool) {
	scope.params = nil
	found := -1
	ast.Inspect(body, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
			return true
		}
		for _, pair := range [][2]ast.Expr{{bin.X, bin.Y}, {bin.Y, bin.X}} {
			id, ok := pair[0].(*ast.Ident)
			if !ok {
				continue
			}
			pi, ok := names[id.Name]
			if !ok || pi == pathParam {
				continue
			}
			if got := evalMethod(pair[1], scope); !got.unknown && len(got.methods) > 0 && (found < 0 || pi < found) {
				found = pi
			}
		}
		return true
	})
	return found, found >= 0
}

// paramIndex maps a function's parameter names to their positions.
func paramIndex(fields *ast.FieldList) map[string]int {
	names := map[string]int{}
	if fields == nil {
		return names
	}
	pos := 0
	for _, field := range fields.List {
		for _, name := range field.Names {
			names[name.Name] = pos
			pos++
		}
		if len(field.Names) == 0 {
			pos++
		}
	}
	return names
}

// seedOnlyDecls returns the declarations of the seed files that no
// declaration outside them reaches through a function or method call.
// Variables are not followed: main.go registers seedCmd, and that is the
// seed command, not a user-facing caller of what it runs.
func seedOnlyDecls(files []*ast.File, paths []string) map[ast.Decl]bool {
	funcs := map[string][]*ast.FuncDecl{}
	methods := map[string][]*ast.FuncDecl{}
	seedOnly := map[ast.Decl]bool{}
	var queue []ast.Decl
	for i, file := range files {
		seed := isCLISeedFile(paths[i])
		for _, decl := range file.Decls {
			if !seed {
				queue = append(queue, decl)
				continue
			}
			seedOnly[decl] = true
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name != "init" {
				if fn.Recv == nil {
					funcs[fn.Name.Name] = append(funcs[fn.Name.Name], fn)
				} else {
					methods[fn.Name.Name] = append(methods[fn.Name.Name], fn)
				}
			}
		}
	}
	if len(seedOnly) == 0 {
		return seedOnly
	}
	visited := map[ast.Decl]bool{}
	for len(queue) > 0 {
		decl := queue[0]
		queue = queue[1:]
		if visited[decl] {
			continue
		}
		visited[decl] = true
		// A function's own name is not a reference to it, and every file's
		// init would otherwise reach every seed file's init.
		var body ast.Node = decl
		if fn, ok := decl.(*ast.FuncDecl); ok {
			if fn.Body == nil {
				continue
			}
			body = fn.Body
		}
		ast.Inspect(body, func(n ast.Node) bool {
			var targets []*ast.FuncDecl
			switch x := n.(type) {
			case *ast.Ident:
				targets = funcs[x.Name]
			case *ast.SelectorExpr:
				targets = methods[x.Sel.Name]
			}
			for _, fn := range targets {
				if !visited[fn] {
					delete(seedOnly, fn)
					queue = append(queue, fn)
				}
			}
			return true
		})
	}
	return seedOnly
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

	pkgs := map[string]*cliPackage{}
	var dirs []string
	// methodSinks is shared by every package: a method wrapper (StreamSSE)
	// is declared in one package (internal/cli) but called from the others
	// (cmd/crewship, internal/cli/tui), and a call site matches it by name
	// wherever the receiver comes from. Ident-named wrappers stay
	// package-local — that is the resolution rule for plain names.
	methodSinks := map[string]sinkSpec{}
	for name, spec := range cliSinkMethods {
		methodSinks[name] = spec
	}
	for i, file := range files {
		dir := filepath.Dir(paths[i])
		if pkgs[dir] == nil {
			pkgs[dir] = &cliPackage{
				env:     newShapeEnv(nil),
				sinks:   &sinkSet{methods: methodSinks, funcs: map[string]sinkSpec{}},
				callers: map[string][]cliCallSite{},
				envs:    map[ast.Decl]*shapeEnv{},
			}
			dirs = append(dirs, dir)
		}
		pkgs[dir].files = append(pkgs[dir].files, file)
	}
	// Pass one: package-level names, per Go package — cmd/crewship (main)
	// and internal/cli (cli) share no identifiers, and resolving a name
	// across the two could build a shape no real caller can. Constants and
	// variables holding a path prefix (`const chatRoomBase =
	// "/api/v1/conversations"`) and helpers that return one (`func
	// chatRoomPath(id string) string { return chatRoomBase + "/" +
	// url.PathEscape(id) }`) are how the CLI avoids repeating prefixes, and
	// a caller that writes `chatRoomPath(id) + "/messages"` only resolves
	// to a route once those are known. Every call to a plain function name
	// is indexed too, for methods that arrive from callers.
	for i, file := range files {
		pkg := pkgs[filepath.Dir(paths[i])]
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						pkg.env.bindValueSpec(value)
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Body != nil {
					pkg.env.bindFunc(d)
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if name, ok := call.Fun.(*ast.Ident); ok {
						pkg.callers[name.Name] = append(pkg.callers[name.Name], cliCallSite{call: call, decl: decl})
					}
				}
				return true
			})
		}
	}
	// Pass one and a half: which functions and methods are request wrappers —
	// pass one of their parameters into the path position of a request call —
	// and what they send. One fixpoint across every package: an ident
	// wrapper binds in its own package only, a method wrapper binds
	// everywhere its receiver travels. The client methods themselves are
	// never re-derived from their bodies.
	for range 16 {
		changed := false
		for _, dir := range dirs {
			pkg := pkgs[dir]
			for _, file := range pkg.files {
				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Body == nil {
						continue
					}
					if _, base := cliSinkMethods[fn.Name.Name]; base && fn.Recv != nil {
						continue
					}
					scope := methodScope{env: pkg.env, pkg: pkg}
					spec, ok := bodySinkSpec(fn.Body, paramIndex(fn.Type.Params), pkg.sinks, scope)
					if !ok {
						continue
					}
					target := pkg.sinks.funcs
					if fn.Recv != nil {
						target = pkg.sinks.methods
					}
					if old, dup := target[fn.Name.Name]; !dup || !old.equal(spec) {
						if dup && fn.Recv != nil {
							spec = unionSpecs(old, spec)
							if old.equal(spec) {
								continue
							}
						}
						target[fn.Name.Name] = spec
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	callbacks := map[*ast.FuncDecl]map[string]sinkSpec{}
	for _, dir := range dirs {
		for fn, params := range callbackRequestSinks(pkgs[dir]) {
			callbacks[fn] = params
		}
	}

	byKey := map[string]int{}
	var shapes []cliPathShape
	for _, dir := range dirs {
		pkg := pkgs[dir]
		var pkgPaths []string
		for i := range files {
			if filepath.Dir(paths[i]) == dir {
				pkgPaths = append(pkgPaths, paths[i])
			}
		}
		seedOnly := seedOnlyDecls(pkg.files, pkgPaths)
		for fi, file := range pkg.files {
			source := pkgPaths[fi]
			for _, decl := range file.Decls {
				if seedOnly[decl] {
					continue
				}
				// Pass two, per top-level declaration: local assignments
				// first, then the request calls in it rendered against local
				// + package names. Declarations rather than functions
				// because most of the CLI is `var xCmd = &cobra.Command{RunE:
				// func(...) error {...}}` — the request is built inside a
				// function literal in a var block. Closure wrappers assigned
				// inside the declaration (`fetch := func(path string) {…}`)
				// are discovered the same way package-level ones were,
				// against this declaration's calls; the copy is per
				// declaration so a closure's name never matches a call in a
				// declaration it does not belong to.
				local := pkg.declEnv(decl)
				scope := methodScope{env: local, pkg: pkg}
				if fn, ok := decl.(*ast.FuncDecl); ok {
					scope.fn = fn
				}
				sinks := pkg.sinks.forDecl()
				if fn, ok := decl.(*ast.FuncDecl); ok {
					for name, spec := range callbacks[fn] {
						sinks.funcs[name] = spec
					}
				}
				discoverDeclSinks(decl, sinks, scope)
				ast.Inspect(decl, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					spec, ok := sinks.spec(call)
					if !ok || spec.path >= len(call.Args) {
						return true
					}
					rendered := local.shapes(call.Args[spec.path], 0)
					var methods methodSet
					resolvedMethods := false
					for _, shape := range rendered {
						shape = normalizeShape(shape)
						if shape == "" {
							continue
						}
						if !resolvedMethods {
							methods = callMethods(call, spec, scope)
							resolvedMethods = true
						}
						key := shape + "\n" + source
						at, seen := byKey[key]
						if !seen {
							at = len(shapes)
							byKey[key] = at
							shapes = append(shapes, cliPathShape{Shape: shape, Source: source})
						}
						entry := &shapes[at]
						entry.Methods = appendUnique(entry.Methods, methods.methods...)
						sort.Strings(entry.Methods)
						if methods.unknown || len(methods.methods) == 0 {
							entry.MethodUnknown = true
							entry.UnknownSites = appendUnique(entry.UnknownSites, fmt.Sprintf("%s:%d", source, fset.Position(call.Pos()).Line))
						}
					}
					return true
				})
			}
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

// cliMethodUnknownSites counts the distinct request calls whose method could
// not be read.
func cliMethodUnknownSites(shapes []cliPathShape) int {
	seen := map[string]bool{}
	for _, shape := range shapes {
		for _, site := range shape.UnknownSites {
			seen[site] = true
		}
	}
	return len(seen)
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
// results holds each return statement's result list, so one result of a
// multi-value helper can be rendered on its own.
type funcShape struct {
	params  []string
	results [][]ast.Expr
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

// resultOf stands for result index of a multi-value call.
func resultOf(call ast.Expr, index int) ast.Expr {
	return &ast.CallExpr{Fun: ast.NewIdent(cliResultMarker), Args: []ast.Expr{call, &ast.BasicLit{Kind: token.INT, Value: strconv.Itoa(index)}}}
}

// bindMulti binds lhs to rhs pairwise, or — for `a, b, err := f()` — each
// name to its result of the one call.
func (e *shapeEnv) bindMulti(lhs []ast.Expr, rhs []ast.Expr, tok token.Token) {
	if len(lhs) != len(rhs) {
		if len(rhs) != 1 || len(lhs) < 2 {
			return
		}
		if _, ok := rhs[0].(*ast.CallExpr); !ok {
			return
		}
		if tok != token.ASSIGN && tok != token.DEFINE {
			return
		}
		for j, l := range lhs {
			if ident, ok := l.(*ast.Ident); ok {
				e.bind(ident.Name, resultOf(rhs[0], j))
			}
		}
		return
	}
	for j, l := range lhs {
		ident, ok := l.(*ast.Ident)
		if !ok {
			continue
		}
		switch tok {
		case token.ADD_ASSIGN:
			e.bind(ident.Name, &ast.BinaryExpr{X: ident, Op: token.ADD, Y: rhs[j]})
		case token.ASSIGN, token.DEFINE:
			e.bind(ident.Name, rhs[j])
		}
	}
}

// bindValueSpec records `const x = v` / `var x = v`, at package level or
// inside a function.
func (e *shapeEnv) bindValueSpec(value *ast.ValueSpec) {
	lhs := make([]ast.Expr, len(value.Names))
	for j, name := range value.Names {
		lhs[j] = name
	}
	if len(value.Values) > 0 {
		e.bindMulti(lhs, value.Values, token.DEFINE)
	}
}

// bindAssignments records every `x := v`, `x = v` and `x += v` under node,
// every function-local `const`/`var`, and each value a `range` over a
// literal yields. The compound form matters: `endpoint += "/source"` is how
// the page project command reaches four routes from one base, and it is
// bound as `endpoint + "/source"` so the earlier bindings of endpoint feed
// into it.
func (e *shapeEnv) bindAssignments(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			e.bindMulti(x.Lhs, x.Rhs, x.Tok)
		case *ast.DeclStmt:
			if gen, ok := x.Decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						e.bindValueSpec(value)
					}
				}
			}
		case *ast.RangeStmt:
			if value, ok := x.Value.(*ast.Ident); ok {
				e.bind(value.Name, &ast.IndexExpr{X: x.X, Index: ast.NewIdent("_")})
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
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if ok && len(ret.Results) >= 1 {
			shape.results = append(shape.results, ret.Results)
		}
		return true
	})
	if len(shape.results) > 0 {
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
				out = append(out, e.sprintfShapes(format, call.Args[1:], depth)...)
			}
			return out
		}
	case *ast.Ident:
		if depth >= maxShapeDepth {
			return []string{cliPathPlaceholder}
		}
		index := 0
		if fn.Name == cliResultMarker {
			// One result of a multi-value call: the inner call, rendered at
			// that result position.
			inner, ok := call.Args[0].(*ast.CallExpr)
			if !ok {
				return []string{cliPathPlaceholder}
			}
			index, _ = strconv.Atoi(call.Args[1].(*ast.BasicLit).Value)
			call = inner
			if fn, ok = inner.Fun.(*ast.Ident); !ok {
				return []string{cliPathPlaceholder}
			}
		}
		if helper := e.lookupFunc(fn.Name); helper != nil {
			callee := newShapeEnv(helper.local)
			for i, param := range helper.params {
				if i < len(call.Args) {
					callee.rendered[param] = e.shapes(call.Args[i], depth+1)
				}
			}
			var out []string
			for _, results := range helper.results {
				if index < len(results) {
					out = append(out, callee.shapes(results[index], depth+1)...)
				}
			}
			if len(out) == 0 {
				return []string{cliPathPlaceholder}
			}
			return out
		}
	}
	return []string{cliPathPlaceholder}
}

// sprintfShapes preserves known string suffixes passed through plain %s
// verbs. Unknown values remain one-segment placeholders. Indexed/dynamic-width
// formats retain the previous conservative rendering rather than guessing which
// argument a verb consumes.
func (e *shapeEnv) sprintfShapes(format string, args []ast.Expr, depth int) []string {
	matches := sprintfVerb.FindAllStringIndex(format, -1)
	if strings.Contains(format, "%%") {
		return []string{strings.ReplaceAll(sprintfVerb.ReplaceAllString(format, cliPathPlaceholder), "%%", "%")}
	}
	for _, at := range matches {
		if at[1]-at[0] != 2 {
			return []string{strings.ReplaceAll(sprintfVerb.ReplaceAllString(format, cliPathPlaceholder), "%%", "%")}
		}
	}
	out, start := []string{""}, 0
	for i, at := range matches {
		values := []string{cliPathPlaceholder}
		if format[at[0]:at[1]] == "%s" && i < len(args) {
			values = e.shapes(args[i], depth)
		}
		var next []string
		for _, prefix := range out {
			for _, value := range values {
				next = append(next, prefix+format[start:at[0]]+value)
			}
		}
		out, start = next, at[1]
	}
	for i := range out {
		out[i] += format[start:]
	}
	return out
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

// cliCallers returns the CLI source files whose requests reach method on
// route. A shape whose calls send that method is a caller. When none does, a
// method-unknown shape on the path still reaches it — the path-level rule —
// and pathOnly reports that the operation is covered only that way.
func cliCallers(method, route string, shapes []cliPathShape, routes map[string]bool) (callers []string, pathOnly bool) {
	normalized := normalizeRoutePath(route)
	var known, unknown []string
	for _, shape := range shapes {
		if !shapeMatchesRoute(shape.Shape, normalized, routes) {
			continue
		}
		switch {
		case contains(shape.Methods, method):
			known = appendUnique(known, shape.Source)
		case shape.MethodUnknown:
			unknown = appendUnique(unknown, shape.Source)
		}
	}
	if len(known) > 0 {
		return limitSignals(known), false
	}
	return limitSignals(unknown), len(unknown) > 0
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

// cliParityExemption is one operation — or, with no Method, every operation
// on a path — the gate does not hold to parity, and why.
type cliParityExemption struct {
	Method string
	Path   string
	Reason string
	Line   int
}

func (ex cliParityExemption) matches(method, path string) bool {
	return (ex.Method == "" || ex.Method == method) && normalizeRoutePath(ex.Path) == normalizeRoutePath(path)
}

func (ex cliParityExemption) String() string {
	if ex.Method == "" {
		return ex.Path
	}
	return ex.Method + " " + ex.Path
}

// cutField splits off the first whitespace-delimited field of s.
func cutField(s string) (field, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	at := strings.IndexFunc(s, unicode.IsSpace)
	if at < 0 {
		return s, ""
	}
	return s[:at], strings.TrimSpace(s[at:])
}

// readCLIParityExemptions parses the exemption file: one line per exemption,
// `[METHOD ]/api/... reason`; `#` lines and blank lines are skipped. A line
// with no method exempts every method on the path. A route with no reason is
// an error, because an exemption nobody can justify is the gate quietly
// shrinking; so is a method that is not an upper-case HTTP method.
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
		method := ""
		first, rest := cutField(line)
		if httpMethods[first] {
			method = first
			first, rest = cutField(rest)
		}
		if !strings.HasPrefix(first, "/api/") || rest == "" {
			return nil, fmt.Errorf("%s:%d: expected `[METHOD ]/api/... <reason>`, got %q", name, lineNo, line)
		}
		out = append(out, cliParityExemption{Method: method, Path: first, Reason: rest, Line: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return out, nil
}

// cliParityFor decides one operation's parity status: covered when a CLI
// request reaches it, exempt when the file says so, missing otherwise. The
// exemption is consulted second so that an operation which gained a CLI keeps
// reporting "cli" — the stale exemption is then listed by
// staleCLIParityExemptions.
func cliParityFor(method, path string, callers []string, exemptions []cliParityExemption) string {
	if len(callers) > 0 {
		return cliParityCovered
	}
	for _, ex := range exemptions {
		if ex.matches(method, path) {
			return cliParityExempt
		}
	}
	return cliParityMissing
}

// staleCLIParityExemptions names exemptions the tree no longer needs: every
// operation they cover has a CLI caller now, or they cover no operation in
// the spec. They are reported rather than failed — the branch that adds the
// CLI should not have to edit this file in the same commit — but every one
// of them is a line that should go.
func staleCLIParityExemptions(exemptions []cliParityExemption, records []apiRecord) []string {
	var out []string
	for _, ex := range exemptions {
		matched, covered := 0, 0
		for _, rec := range records {
			if ex.matches(rec.Method, rec.Path) {
				matched++
				if rec.CLIParity == cliParityCovered {
					covered++
				}
			}
		}
		switch {
		case matched == 0:
			out = append(out, fmt.Sprintf("%s:%d: %s is not in the OpenAPI document — remove the exemption", cliParityExemptionsPath, ex.Line, ex))
		case matched == covered:
			out = append(out, fmt.Sprintf("%s:%d: %s now has a CLI caller — remove the exemption", cliParityExemptionsPath, ex.Line, ex))
		}
	}
	return out
}

// cliParitySummaryLine is the gate's one-line summary: the path-level counts
// it has always printed, then the operation-level ones it enforces, then how
// much of the coverage rests on calls whose method could not be read.
func cliParitySummaryLine(r report) string {
	s := r.Summary
	return fmt.Sprintf("CLI parity %d API paths, %d with no command, %d exempt; %d operations, %d with no command for their method, %d exempt; %d reached by path only through %d method-unknown call sites",
		countPaths(r.API), s.APIWithoutCLI, s.APIExemptFromCLI,
		len(r.API), s.APIOperationsWithoutCLI, s.APIOperationsExemptFromCLI,
		s.APIOperationsCLIPathOnly, s.CLIMethodUnknownSites)
}
