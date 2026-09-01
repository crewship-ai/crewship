// Package journalgen scans internal/journal/types.go for its EntryType const
// declarations. It is the single piece of logic both cmd/gen-journal-registry
// (which writes internal/journal/registry_generated.go) and
// internal/journal's own drift test import — so the generator and the test
// that catches drift from it can never independently disagree about what
// counts as "every entry type in the source". Only the source itself, read
// fresh each time, can disagree with either of them, which is exactly the
// drift the test exists to catch.
//
// This is a source scan, not reflection: EntryType is a plain string type
// with no runtime registry of its own (that is the whole defect A3 fixes),
// so "every declared value" can only be answered by reading the const block.
package journalgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
)

// EntryConst is one `Name EntryType = "value"` declaration found in the
// source.
type EntryConst struct {
	Name  string // Go identifier, e.g. "EntryMissionStatus"
	Value string // the string literal, e.g. "mission.status_change"
}

// entryTypeIdent is the type name the scanner looks for on each ValueSpec.
// journal.EntryType is declared `type EntryType string` in the same file, so
// every constant in its block is typed with the bare identifier "EntryType"
// (no package qualifier — the scan runs inside the journal package's own
// source).
const entryTypeIdent = "EntryType"

// Scan parses the Go source file at path and returns every constant declared
// with type EntryType, sorted by Value so the result is stable regardless of
// how the source groups or reorders its const blocks.
//
// It deliberately only recognises the literal shape `Name EntryType =
// "value"` — one name, one explicit type, one string literal. types.go
// declares all 100+ of its entries that way (never via iota or an implicit
// repeated type), and requiring the explicit shape means a future constant
// declared some other way fails LOUD (an empty scan result, caught by the
// drift test) rather than being silently skipped.
func Scan(path string) ([]EntryConst, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("journalgen: parse %s: %w", path, err)
	}

	var out []EntryConst
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != entryTypeIdent {
				continue
			}
			if len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return nil, fmt.Errorf("journalgen: %s: %s is typed EntryType but not a string literal",
						fset.Position(vs.Pos()), name.Name)
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return nil, fmt.Errorf("journalgen: %s: unquote %s: %w", fset.Position(lit.Pos()), name.Name, err)
				}
				out = append(out, EntryConst{Name: name.Name, Value: value})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out, nil
}
