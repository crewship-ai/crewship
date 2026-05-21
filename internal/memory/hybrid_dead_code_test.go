package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHybridSearch_NotYetWiredToDispatcher is a sentinel test that
// asserts memory.HybridSearch is NOT reachable from the in-process
// tool dispatcher (Dispatcher.handleSearch in tools.go). The HTTP +
// sidecar paths (api/memory_hybrid_search_handler.go,
// sidecar/memory.go) DO call HybridSearch — those are the wired
// surfaces — but the dispatcher that serves the model's
// memory.search tool call still uses a substring-only scan.
//
// Why a test? Without one, hybrid.go reads like dead code to anyone
// browsing the package and they're tempted to delete it. The
// sentinel says: "this is not dead, it's reachable from elsewhere
// in the tree, and the wiring to handleSearch is deliberately
// deferred to PR-F5." If this test ever fails (because someone
// wired HybridSearch into handleSearch), flip the assertion and
// remove the tombstone comments in:
//
//   - internal/memory/tools.go (handleSearch TODO(PR-F5))
//   - internal/memory/hybrid.go (file-level header)
//
// Implementation note: we parse tools.go with go/parser instead of a
// reflective call-graph walk because the dispatcher's call graph at
// runtime is unknowable without an actual call — but a static
// "does the source of handleSearch contain HybridSearch?" scan is
// precisely the invariant the tombstone documents.
func TestHybridSearch_NotYetWiredToDispatcher(t *testing.T) {
	// Locate tools.go relative to the test file. The test runs from
	// the package directory under `go test`, so a plain relative
	// path is fine and avoids a brittle module-root lookup.
	src, err := os.ReadFile(filepath.Join(".", "tools.go"))
	if err != nil {
		t.Fatalf("reading tools.go: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "tools.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing tools.go: %v", err)
	}

	// Find the handleSearch method on *Dispatcher and walk its body
	// for any identifier or selector named "HybridSearch". An
	// import-only reference doesn't count — we want call sites.
	var handleSearchBody *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "handleSearch" {
			continue
		}
		handleSearchBody = fn.Body
		break
	}
	if handleSearchBody == nil {
		t.Fatalf("Dispatcher.handleSearch not found in tools.go — has the function been renamed? If so, update this sentinel test.")
	}

	hits := []string{}
	ast.Inspect(handleSearchBody, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == "HybridSearch" {
				hits = append(hits, fset.Position(x.Pos()).String())
			}
		case *ast.SelectorExpr:
			if x.Sel != nil && x.Sel.Name == "HybridSearch" {
				hits = append(hits, fset.Position(x.Sel.Pos()).String())
			}
		}
		return true
	})

	if len(hits) > 0 {
		t.Fatalf(`HybridSearch IS now wired into Dispatcher.handleSearch — sentinel tripped.
Flip this test assertion and remove the tombstone comments in:
  - internal/memory/tools.go (handleSearch TODO PR-F5 block)
  - internal/memory/hybrid.go (file-level package header)
Call sites found: %v`, hits)
	}

	// Source-level smoke: handleSearch's substring path should still
	// be the implementation (not a delegation stub). If someone
	// deletes substring search entirely without wiring HybridSearch,
	// this check catches it before runtime.
	bodyText := string(src)
	if !strings.Contains(bodyText, "strings.Contains(strings.ToLower(line), needle)") {
		t.Errorf("handleSearch substring scan signature missing — search path may be in mid-refactor. Re-confirm dispatcher search behaviour and update this sentinel.")
	}
}
