// Discover tagged tests with Go's parser instead of source-text patterns.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func hasActualTag(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return e.Tag == "mooring_rust_integration"
	case *constraint.NotExpr:
		return hasActualTag(e.X)
	case *constraint.AndExpr:
		return hasActualTag(e.X) || hasActualTag(e.Y)
	case *constraint.OrExpr:
		return hasActualTag(e.X) || hasActualTag(e.Y)
	}
	return false
}

func discover(root string) (map[string]string, error) {
	found := make(map[string]string)
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		tagged := false
		for _, group := range file.Comments {
			for _, comment := range group.List {
				if comment.Pos() >= file.Package || (!constraint.IsGoBuild(comment.Text) && !constraint.IsPlusBuild(comment.Text)) {
					continue
				}
				expr, err := constraint.Parse(comment.Text)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				tagged = tagged || hasActualTag(expr)
			}
		}
		if !tagged {
			return nil
		}
		testingName := ""
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if value == "testing" {
				testingName = "testing"
				if imported.Name != nil {
					testingName = imported.Name.Name
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			name := fn.Name.Name
			if len(name) > len("Test") {
				first, _ := utf8.DecodeRuneInString(name[len("Test"):])
				if unicode.IsLower(first) {
					continue // Same test-name boundary as cmd/go.
				}
			}
			valid := fn.Type.TypeParams == nil && (fn.Type.Results == nil || fn.Type.Results.NumFields() == 0) && fn.Type.Params.NumFields() == 1
			if valid {
				param := fn.Type.Params.List[0]
				star, ok := param.Type.(*ast.StarExpr)
				valid = ok && len(param.Names) <= 1
				if valid {
					if testingName == "." {
						ident, ok := star.X.(*ast.Ident)
						valid = ok && ident.Name == "T"
					} else {
						selector, ok := star.X.(*ast.SelectorExpr)
						valid = ok && selector.Sel.Name == "T"
						if valid {
							ident, ok := selector.X.(*ast.Ident)
							valid = ok && testingName != "" && ident.Name == testingName
						}
					}
				}
			}
			if !valid {
				return fmt.Errorf("unsupported tagged test declaration: %s: %s", path, name)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if _, exists := found[name]; exists {
				return fmt.Errorf("duplicate source actual test: %s", name)
			}
			found[name] = filepath.ToSlash(rel)
		}
		return nil
	})
	return found, err
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mooring-actual-discover ROOT")
		os.Exit(1)
	}
	found, err := discover(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(found); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
