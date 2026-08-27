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
			// Only structs that are serialized at all: at least one field
			// carries a json tag. Without this the check would sweep every
			// embedding in the package, most of which never meets a
			// formatter.
			if !hasJSONTaggedField(st) {
				return true
			}
			for _, field := range st.Fields.List {
				// Embedded fields have no name.
				if len(field.Names) != 0 {
					continue
				}
				var tag string
				if field.Tag != nil {
					unquoted, err := strconv.Unquote(field.Tag.Value)
					if err != nil {
						continue
					}
					tag = unquoted
				}
				jsonTag := reflect.StructTag(tag).Get("json")
				// A tag that renames the field ends the flattening, and with
				// it the whole problem — the field becomes an ordinary named
				// key in both formats.
				if jsonTag != "" && !strings.Contains(jsonTag, "inline") &&
					!strings.HasPrefix(jsonTag, ",") {
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
					t.Errorf("%s:%d: embedded %s is flattened into -f json but nested by -f yaml; "+
						"add `yaml:\",inline\"` (and `json:\",inline\"` to say so out loud).",
						path, pos.Line, name)
				}
			}
			return true
		})
	}

	// Anti-vacuity: this file is worthless if the extractor stops finding the
	// shape it guards. personaView is the known instance.
	if checked == 0 {
		t.Fatal("found no embedded fields in json-tagged structs at all — the extractor has gone blind")
	}
	t.Logf("checked %d embedded field(s) in json-serialized structs", checked)
}

// hasJSONTaggedField reports whether any field of st carries a `json:` tag,
// which is how this file tells a serialization shape from an ordinary struct
// that merely embeds something.
func hasJSONTaggedField(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			continue
		}
		if _, ok := reflect.StructTag(tag).Lookup("json"); ok {
			return true
		}
	}
	return false
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
