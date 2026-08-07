package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/moby/moby/client"
)

// dockerClientImport is the import path of the Docker SDK client. A package
// cannot hold a live Docker connection without importing it: every value that
// reaches the daemon is a *client.Client, or an interface spelled in terms of
// the option and result types this package defines.
const dockerClientImport = "github.com/moby/moby/client"

// skipDirs are directory names never walked.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"testdata":     true,
	"web":          true,
	"docs":         true,
	"e2e":          true,
	"tests":        true,
}

// mobyMethods is the exported method set of *client.Client, read by reflection
// rather than transcribed. A moby upgrade that adds or renames a method changes
// this set automatically, so the gate cannot go stale against the SDK.
func mobyMethods() map[string]bool {
	out := make(map[string]bool)
	t := reflect.TypeOf(&client.Client{})
	for i := 0; i < t.NumMethod(); i++ {
		out[t.Method(i).Name] = true
	}
	return out
}

// Call is one call site of a Docker SDK method that issues an HTTP request.
type Call struct {
	Method   string // SDK method name, e.g. "ContainerCreate"
	Pkg      string // package directory relative to the repo root
	File     string // file path relative to the repo root
	Line     int
	Receiver string // rightmost identifier of the receiver, e.g. "client"
}

// Shellout is a place where a docker-compatible CLI is executed with the
// daemon endpoint pinned. These reach the same socket without going through the
// SDK, so the allow-list has to account for them or they fail at run time with
// a 403 that no compile-time check would have caught.
type Shellout struct {
	Pkg  string
	File string
	Line int
}

// Surface is the Docker reach derived from the tree.
type Surface struct {
	Packages  []string            // packages importing the Docker SDK client
	Calls     []Call              // every SDK call site, sorted
	Methods   map[string][]string // SDK method -> packages calling it
	Shellouts []Shellout          // daemon-pinned CLI executions
}

// Scan derives the Docker API surface of the tree rooted at root.
func Scan(root string) (*Surface, error) {
	methods := mobyMethods()

	pkgDirs, err := dockerImportingDirs(root)
	if err != nil {
		return nil, err
	}

	s := &Surface{Methods: map[string][]string{}}
	s.Packages = append(s.Packages, pkgDirs...)
	sort.Strings(s.Packages)

	fset := token.NewFileSet()
	for _, dir := range pkgDirs {
		files, err := goFiles(root, dir)
		if err != nil {
			return nil, err
		}
		for _, rel := range files {
			f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", rel, err)
			}
			s.Calls = append(s.Calls, callsInFile(fset, f, methods, dir, rel)...)
		}
	}

	sort.Slice(s.Calls, func(i, j int) bool {
		a, b := s.Calls[i], s.Calls[j]
		switch {
		case a.Method != b.Method:
			return a.Method < b.Method
		case a.File != b.File:
			return a.File < b.File
		default:
			return a.Line < b.Line
		}
	})

	byMethod := map[string]map[string]bool{}
	for _, c := range s.Calls {
		if byMethod[c.Method] == nil {
			byMethod[c.Method] = map[string]bool{}
		}
		byMethod[c.Method][c.Pkg] = true
	}
	for m, pkgs := range byMethod {
		s.Methods[m] = sortedKeys(pkgs)
	}

	s.Shellouts, err = scanShellouts(root)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// dockerImportingDirs returns every package directory (relative to root, slash
// separated) with a non-test file importing the Docker SDK client.
func dockerImportingDirs(root string) ([]string, error) {
	found := map[string]bool{}
	fset := token.NewFileSet()

	err := walkGo(root, func(path, rel string) error {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == dockerClientImport {
				found[filepath.ToSlash(filepath.Dir(rel))] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sortedKeys(found), nil
}

// callsInFile reports every call whose selector names an SDK method that issues
// an HTTP request.
//
// Deliberately name-driven rather than type-driven. Resolving receiver types
// needs whole-program type information, which means type-checking internal/api
// on every run; a name-driven scan over the few packages that import the SDK
// over-reports instead of under-reporting, and over-reporting fails loudly
// where under-reporting fails silently. The two places where a name alone is
// genuinely ambiguous — SDK methods sharing a name with slog.Logger.Info,
// sql.DB.Ping, fs.DirEntry.Info — are narrowed by receiver, and both the
// ambiguous set and the receivers that count as Docker clients are reviewed
// data in allowlist.go.
func callsInFile(fset *token.FileSet, f *ast.File, methods map[string]bool, pkgDir, relPath string) []Call {
	var out []Call
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if !methods[name] || localMethods[name] {
			return true
		}
		recv := rightmostIdent(sel.X)
		if ambiguousMethods[name] && !dockerReceivers[recv] {
			return true
		}
		out = append(out, Call{
			Method:   name,
			Pkg:      pkgDir,
			File:     relPath,
			Line:     fset.Position(sel.Sel.Pos()).Line,
			Receiver: recv,
		})
		return true
	})
	return out
}

// rightmostIdent reduces a receiver expression to the identifier naming the
// value called on: p.client -> "client", m.Client -> "Client", cli -> "cli".
func rightmostIdent(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.CallExpr:
		return rightmostIdent(x.Fun)
	case *ast.IndexExpr:
		return rightmostIdent(x.X)
	case *ast.StarExpr:
		return rightmostIdent(x.X)
	case *ast.ParenExpr:
		return rightmostIdent(x.X)
	case *ast.TypeAssertExpr:
		return rightmostIdent(x.X)
	}
	return ""
}

// scanShellouts finds executions of a docker-compatible CLI in files that also
// pin the daemon endpoint by setting DOCKER_HOST in the child environment.
// Reading DOCKER_HOST does not qualify — the provider does that to find the
// daemon for the SDK. Setting it is what makes a subprocess a second client of
// the same socket, which is the case a proxy has to cover.
func scanShellouts(root string) ([]Shellout, error) {
	var out []Shellout
	fset := token.NewFileSet()

	err := walkGo(root, func(path, rel string) error {
		src, err := os.ReadFile(path) //nolint:gosec // paths come from the walk
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), `"DOCKER_HOST="`) {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", rel, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "CommandContext" && sel.Sel.Name != "Command") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
				return true
			}
			out = append(out, Shellout{
				Pkg:  filepath.ToSlash(filepath.Dir(rel)),
				File: filepath.ToSlash(rel),
				Line: fset.Position(call.Pos()).Line,
			})
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// goFiles lists the non-test Go files of one package directory, relative to
// root and slash separated.
func goFiles(root, dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Join(dir, name)))
	}
	sort.Strings(out)
	return out, nil
}

// walkGo calls fn for every non-test Go file under root, passing the absolute
// path and the path relative to root.
func walkGo(root string, fn func(path, rel string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		return fn(path, filepath.ToSlash(rel))
	})
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
