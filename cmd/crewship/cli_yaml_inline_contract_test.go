package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// An embedded struct rendered under `-f yaml` must be exported and carry
// `yaml:",inline"`.
//
// yaml.v3 does not read `json:` tags, does not auto-inline an embedded struct,
// and cannot reflect into an *unexported* embedded field at all. So a type
// written for JSON as
//
//	type personaView struct {
//	    Kind            string `json:"kind"`
//	    personaResponse `json:",inline"`   // unexported type
//	}
//
// serializes fine as JSON and, under `-f yaml`, panics with
// "reflect.Value.Interface: cannot return value obtained from unexported field
// or method". `crewship persona view <agent> -f yaml` and
// `persona crew <slug> show -f yaml` did exactly that.
//
// The sibling guard in cli_format_contract_test.go is a static check that a
// command *honours* the format flag; it never executes one, so a format that
// crashes on render is invisible to it. This closes that specific gap
// statically, which is the only way to cover it without driving every command.
func TestEmbeddedJSONInlineIsAlsoYAMLSafe(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				// Embedded fields have no name.
				if len(field.Names) != 0 || field.Tag == nil {
					continue
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					continue
				}
				if !strings.Contains(reflect.StructTag(tag).Get("json"), "inline") {
					continue
				}
				checked++

				pos := fset.Position(field.Pos())
				name := embeddedTypeName(field.Type)

				if name != "" && !ast.IsExported(name) {
					t.Errorf("%s:%d: embedded %s is unexported but inlined into JSON output; "+
						"yaml.v3 cannot reflect into it and `-f yaml` panics. Export the type.",
						path, pos.Line, name)
				}
				if !strings.Contains(reflect.StructTag(tag).Get("yaml"), "inline") {
					t.Errorf("%s:%d: embedded %s has `json:\",inline\"` but no `yaml:\",inline\"`; "+
						"under `-f yaml` its fields nest under the type name instead of inlining.",
						path, pos.Line, name)
				}
			}
			return true
		})
	}

	// Anti-vacuity: this file is worthless if the extractor stops finding the
	// shape it guards. personaView is the known instance.
	if checked == 0 {
		t.Fatal("found no embedded `json:\",inline\"` fields at all — the extractor has gone blind")
	}
	t.Logf("checked %d embedded json-inline field(s)", checked)
}

// embeddedTypeName returns the type name of an embedded field, unwrapping a
// pointer. Qualified types (pkg.T) are reported by their selector, since that
// is what determines exportedness for reflection.
func embeddedTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return embeddedTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}
