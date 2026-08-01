package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestHybridSearch_WiredIntoDispatcher is the flipped form of the old
// PR-F5 tombstone. That sentinel asserted memory.HybridSearch was NOT
// reachable from the tool dispatcher — hybrid.go's header said the
// wiring was deferred, so the test existed to stop anyone deleting
// hybrid.go as dead code while the model's memory.search ran a
// substring scan over a fixed file list.
//
// #1651 landed the wiring, so the invariant inverts: HybridSearch MUST
// stay reachable from Dispatcher.handleSearch. Losing that call is a
// silent quality regression — the tool still answers, just from a
// lowercase substring match against the same files, which cannot match
// two terms in a different order than the file wrote them. No runtime
// test catches "it fell back", because falling back is a legitimate
// degraded mode for a dispatcher with no engine wired.
//
// The check is static for the same reason the original was: the
// dispatcher's runtime call graph is unknowable without an actual call,
// but "does the search path's source reach HybridSearch?" is exactly
// the invariant. Reachability is transitive across functions declared
// in tools.go, so the call may live in a helper (it does —
// searchIndexed) without this sentinel having to name it.
func TestHybridSearch_WiredIntoDispatcher(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(".", "tools.go"))
	if err != nil {
		t.Fatalf("reading tools.go: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tools.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing tools.go: %v", err)
	}

	bodies := map[string]*ast.BlockStmt{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		bodies[fn.Name.Name] = fn.Body
	}
	if bodies["handleSearch"] == nil {
		t.Fatalf("Dispatcher.handleSearch not found in tools.go — has the function been renamed? If so, update this sentinel.")
	}

	// Walk out from handleSearch over same-file callees, collecting the
	// functions reached and every name mentioned along the way.
	reached := map[string]bool{}
	mentioned := map[string]bool{}
	var visit func(fn string)
	visit = func(fn string) {
		if reached[fn] {
			return
		}
		reached[fn] = true
		body := bodies[fn]
		if body == nil {
			return
		}
		ast.Inspect(body, func(n ast.Node) bool {
			name := ""
			switch x := n.(type) {
			case *ast.Ident:
				name = x.Name
			case *ast.SelectorExpr:
				if x.Sel != nil {
					name = x.Sel.Name
				}
			}
			if name == "" {
				return true
			}
			mentioned[name] = true
			if _, isLocal := bodies[name]; isLocal {
				visit(name)
			}
			return true
		})
	}
	visit("handleSearch")

	if !mentioned["HybridSearch"] {
		t.Errorf(`SENTINEL TRIPPED: Dispatcher.handleSearch no longer reaches memory.HybridSearch.
The model's memory.search is back to a substring scan while the FTS5 index the
sidecar maintains goes unread — the #1651 defect. Re-wire it; if the ranked
path was deliberately removed, delete this test and say why in the commit.
Reached from handleSearch: %v`, sortedNames(reached))
	}

	// The substring scan is the documented fallback for a dispatcher
	// with no engine wired (LocalDispatcher, a sidecar whose SQLite open
	// failed). Deleting it turns that case from "unranked results" into
	// "no results", so it has to stay reachable too.
	if !mentioned["candidateFiles"] {
		t.Errorf(`SENTINEL TRIPPED: the substring fallback is no longer reachable from handleSearch.
A dispatcher built without an FTS5 engine would return nothing instead of
degrading to a scan. Reached from handleSearch: %v`, sortedNames(reached))
	}
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
