package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestConsolidateRunnerIsWiredToStorageBasePath is a source-level wiring
// check, for the reason #1663 needed one: the bug was not a wrong value,
// it was a value that was never plumbed at all. RunnerOptions was
// constructed with only BlobRoot, the crew memory root fell to a
// container-absolute default, and the host process created
// /crew/shared/.memory at the filesystem root on every tick. Nothing
// failed, nothing logged, and no unit test could see it — a missing
// struct field is invisible to every test that constructs the struct
// itself.
//
// So this asserts the call site, which is where the omission lived.
func TestConsolidateRunnerIsWiredToStorageBasePath(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server_lifecycle.go", nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parsing server_lifecycle.go: %v", err)
	}

	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "StartBackground" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "consolidate" {
			return true
		}
		found = true
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "StorageBasePath" {
					// The value must come from config, not a literal:
					// a hardcoded path is how this broke.
					if basic, isLit := kv.Value.(*ast.BasicLit); isLit {
						t.Errorf(`consolidate.StartBackground is given a hardcoded StorageBasePath %s.
It must be cfg.Storage.BasePath — the same root the container provider bind-mounts —
or the consolidator writes where no container reads (#1663).`, basic.Value)
					}
					if !strings.Contains(exprString(kv.Value), "Storage.BasePath") {
						t.Errorf("StorageBasePath = %s, want cfg.Storage.BasePath", exprString(kv.Value))
					}
					return false
				}
			}
			t.Errorf(`consolidate.StartBackground's RunnerOptions has no StorageBasePath.
Without it the runner has no host root to resolve each crew's memory tree under and
consolidation writes nothing — or, before #1663, wrote to the host filesystem root.`)
			return false
		}
		return false
	})
	if !found {
		t.Fatalf("consolidate.StartBackground call not found in server_lifecycle.go — moved? update this wiring check.")
	}
}

// exprString renders a selector chain like cfg.Storage.BasePath back to
// source text. Enough for the shapes this file's assertion sees.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.BasicLit:
		return v.Value
	default:
		return "<expr>"
	}
}
