// Package journalgen finds every journal.EntryType constant declared or used
// across the codebase. It is the single piece of logic both
// cmd/gen-journal-registry (which writes internal/journal/registry_generated.go)
// and internal/journal's own drift test import — so the generator and the
// test that catches drift from it can never independently disagree about
// what counts as "every entry type that is really used".
//
// # Why a tree scan, not just types.go
//
// A3 (PRD-ISSUES-AND-ROUTINES-2026 §17) closed the registry on the
// assumption that every entry type is declared in
// internal/journal/types.go's `Name EntryType = <string literal>` const
// block. That assumption was false: at least a dozen entry types are
// declared ad hoc in the packages that emit them —
// internal/api/pages_public_tokens.go, internal/harbormaster/reward.go,
// internal/api/pages_transfer_owner.go and others — using shapes types.go
// never uses: a call-conversion `journal.EntryType(<string literal>)`, or a
// package-level const explicitly typed `journal.EntryType`. A scanner that
// only reads types.go cannot see them, so the "closed" registry rejected
// event types that genuinely fired.
//
// ScanTree recognises both of those shapes, anywhere under the given root
// directories, in addition to types.go's own shape (which is exactly the
// qualified/unqualified-type case with the package name "journal"). One
// shape it CANNOT recognise soundly is a bare string literal assigned to a
// field or parameter typed journal.EntryType with no "EntryType" token
// anywhere in the expression (Go's untyped-constant assignability allows
// `Type: "policy.changed"` inside a journal.Entry{} literal with no
// conversion). That shape has no textual anchor: a scan for it would have to
// treat every string literal assigned to any field named "Type" in the
// entire codebase as a candidate, and grepping for that shows dozens of
// unrelated hits (WebSocket message types, credential kinds, chat event
// kinds) for every real one. Rather than accept that false-positive rate,
// the two call sites that used to write EntryType values that way
// (internal/api/crew_policy.go, internal/api/approvals_handler.go) were
// changed to declare a typed const instead — the same shape every other ad
// hoc entry type in the codebase already uses — which makes them soundly
// scannable like everything else.
package journalgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// EntryConst is one journal.EntryType declaration or literal use found in
// the source.
type EntryConst struct {
	Name string // Go identifier, e.g. "EntryMissionStatus" — empty if the value was never bound to one (an inline conversion at an emit site).
	// Value is the string literal, e.g. "mission.status_change".
	Value string
	// InJournalPkg is true when Name was declared inside package journal
	// itself — the only case where the generated registry may reference Name
	// as a bare Go symbol rather than re-emitting the literal, since a
	// symbol declared in another package is either unexported (inaccessible)
	// or would require internal/journal to import a package that in every
	// observed case already imports internal/journal.
	InJournalPkg bool
	// Pos is file:line of the first occurrence found, for error messages.
	Pos string
}

// entryTypeIdent is the type name the scanner looks for on an unqualified
// EntryType reference. journal.EntryType is declared `type EntryType
// string`; inside the journal package itself, every reference to it is
// necessarily unqualified.
const entryTypeIdent = "EntryType"

// journalPkgName is the package-clause name the scanner requires before it
// treats a bare "EntryType" identifier (not qualified "journal.EntryType")
// as a reference to journal.EntryType. It is a name match, not an import
// resolution — sufficient here because internal/journal's own package really
// is named "journal", and no other package in this tree declares a type
// named EntryType (a false match would require both).
const journalPkgName = "journal"

// Scan parses the Go source file at path and returns every constant declared
// there with an explicit type of EntryType (bare, so only meaningful for a
// file inside package journal — see journalPkgName). Kept as a thin
// convenience over the same per-file logic ScanTree uses, for callers (and
// tests) that only care about types.go in isolation.
func Scan(path string) ([]EntryConst, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("journalgen: parse %s: %w", path, err)
	}
	return scanFile(fset, file)
}

// ScanTree walks every .go file (skipping _test.go files and hidden/vendor
// directories) under the given root directories, relative to dir, and
// returns the deduplicated union of every EntryType value found — by
// value, so a value declared with a name in one place and used anonymously
// in another collapses to a single entry, preferring the named,
// in-package-journal form for codegen.
//
// This is the authoritative "every entry type actually in play" answer: the
// generator builds registry_generated.go from it, and the drift test in
// internal/journal re-runs it to fail the build if an emitter anywhere uses
// a value the registry does not carry.
func ScanTree(dir string, roots ...string) ([]EntryConst, error) {
	fset := token.NewFileSet()
	var all []EntryConst
	for _, root := range roots {
		full := filepath.Join(dir, root)
		err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if name != "." && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				if name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			// Parse from a path relative to dir (repo root, normally) rather
			// than the absolute walk path: the result lands in fset,
			// generated code, and the drift test's error messages, none of
			// which should bake in the absolute filesystem layout of
			// whatever machine or worktree ran the scan.
			display := path
			if rel, rerr := filepath.Rel(dir, path); rerr == nil {
				display = rel
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return fmt.Errorf("journalgen: read %s: %w", path, rerr)
			}
			file, perr := parser.ParseFile(fset, display, src, parser.ParseComments)
			if perr != nil {
				return fmt.Errorf("journalgen: parse %s: %w", path, perr)
			}
			found, serr := scanFile(fset, file)
			if serr != nil {
				return serr
			}
			all = append(all, found...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return dedupeByValue(all), nil
}

// dedupeByValue collapses entries that share a Value, preferring (in order)
// a name declared inside package journal, then any name, then whichever
// occurrence sorts first by Pos — so the result is deterministic regardless
// of filesystem walk order.
func dedupeByValue(in []EntryConst) []EntryConst {
	best := map[string]EntryConst{}
	for _, c := range in {
		cur, ok := best[c.Value]
		if !ok || better(c, cur) {
			best[c.Value] = c
		}
	}
	out := make([]EntryConst, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

func better(a, b EntryConst) bool {
	arank := rank(a)
	brank := rank(b)
	if arank != brank {
		return arank > brank
	}
	return a.Pos < b.Pos
}

func rank(c EntryConst) int {
	switch {
	case c.InJournalPkg && c.Name != "":
		return 2
	case c.Name != "":
		return 1
	default:
		return 0
	}
}

// scanFile extracts every EntryType declaration or literal use from one
// already-parsed file. It looks for two independent shapes and merges them
// (a later dedupe pass, not this function, resolves any overlap):
//
//  1. A const or var ValueSpec whose declared or inferred type is EntryType
//     (bare, inside package journal) or journal.EntryType (qualified,
//     anywhere) — types.go's canonical shape, and the typed-const shape used
//     by internal/api/pages_transfer_owner.go, onboarding_proposal.go and
//     pages_data.go.
//  2. Any EntryType(...) / journal.EntryType(...) conversion call, anywhere
//     in the file — a const/var initializer (pages_webhooks.go,
//     pages_public_tokens.go) or an inline expression at an emit site
//     (harbormaster/reward.go, assignments_stuck_sweeper.go).
//
// A ValueSpec that is unmistakably typed EntryType but does not decompose
// into a plain string (a length mismatch from an implicit-repeat const line,
// a non-literal value, a value that is neither a literal nor a recognised
// conversion) is a hard error, not a skip: silently continuing is exactly
// the failure mode that let eleven real entry types vanish from the registry
// before this fix.
func scanFile(fset *token.FileSet, file *ast.File) ([]EntryConst, error) {
	inJournal := file.Name.Name == journalPkgName
	var out []EntryConst

	// Shape 2 first: a generic walk for every conversion call, wherever it
	// appears (this also finds the ones nested inside a ValueSpec handled by
	// shape 1 below — dedupeByValue resolves the overlap by value).
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isEntryTypeFunc(call.Fun, inJournal) {
			return true
		}
		if len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		out = append(out, EntryConst{Value: value, Pos: fset.Position(call.Pos()).String()})
		return true
	})

	// Shape 1: const/var blocks with an explicit EntryType/journal.EntryType
	// type, one name per literal value.
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		var lastType ast.Expr
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			specType := vs.Type
			if specType == nil && len(vs.Values) == 0 {
				// Implicit repetition of the previous spec's type (and
				// value) in the same block — legal Go, never used for
				// EntryType today. Treated as EntryType-typed (and
				// therefore reported) only if the spec it inherits from was;
				// erroring here rather than silently reusing the previous
				// value, because the inherited value would be a duplicate of
				// the prior constant's, not a new one, and a reader adding a
				// line like this almost certainly meant to give it its own
				// literal.
				if isEntryTypeType(lastType, inJournal) {
					return nil, fmt.Errorf("journalgen: %s: %s implicitly repeats the previous spec's type and value inside a const/var block — give it an explicit `EntryType = \"...\"` instead so the scanner (and a reader) can see its value",
						fset.Position(vs.Pos()), nameList(vs.Names))
				}
				continue
			}
			if specType != nil {
				lastType = specType
			}
			if !isEntryTypeType(specType, inJournal) {
				// Not explicitly typed EntryType — but the value may still
				// be an EntryType(...) conversion inferring the type, as in
				// `journalPageWebhookIssued = journal.EntryType(<its string literal>)`
				// (internal/api/pages_webhooks.go). The generic CallExpr
				// walk above already records the *value* for that shape;
				// this only adds the *name*, for a nicer generated comment
				// — best-effort, so an unmatched value here is not an
				// error, since without an explicit type this spec was never
				// positively identified as being about EntryType at all.
				if len(vs.Names) == len(vs.Values) {
					for i, name := range vs.Names {
						call, ok := vs.Values[i].(*ast.CallExpr)
						if !ok || !isEntryTypeFunc(call.Fun, inJournal) || len(call.Args) != 1 {
							continue
						}
						lit, ok := call.Args[0].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						value, err := strconv.Unquote(lit.Value)
						if err != nil {
							continue
						}
						out = append(out, EntryConst{
							Name:         name.Name,
							Value:        value,
							InJournalPkg: inJournal,
							Pos:          fset.Position(vs.Pos()).String(),
						})
					}
				}
				continue
			}
			if len(vs.Names) != len(vs.Values) {
				return nil, fmt.Errorf("journalgen: %s: %s is typed EntryType but its const/var spec has %d name(s) and %d value(s) — expected one literal per name",
					fset.Position(vs.Pos()), nameList(vs.Names), len(vs.Names), len(vs.Values))
			}
			for i, name := range vs.Names {
				value, err := entryTypeLiteral(vs.Values[i], inJournal)
				if err != nil {
					return nil, fmt.Errorf("journalgen: %s: %s: %w", fset.Position(vs.Pos()), name.Name, err)
				}
				out = append(out, EntryConst{
					Name:         name.Name,
					Value:        value,
					InJournalPkg: inJournal,
					Pos:          fset.Position(vs.Pos()).String(),
				})
			}
		}
	}

	return out, nil
}

// isEntryTypeFunc reports whether fun is the conversion function EntryType
// (bare, package journal only) or journal.EntryType (qualified).
func isEntryTypeFunc(fun ast.Expr, inJournal bool) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return inJournal && f.Name == entryTypeIdent
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == journalPkgName && f.Sel.Name == entryTypeIdent
	default:
		return false
	}
}

// isEntryTypeType reports whether t is the type EntryType (bare, package
// journal only) or journal.EntryType (qualified).
func isEntryTypeType(t ast.Expr, inJournal bool) bool {
	switch e := t.(type) {
	case *ast.Ident:
		return inJournal && e.Name == entryTypeIdent
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && pkg.Name == journalPkgName && e.Sel.Name == entryTypeIdent
	default:
		return false
	}
}

// entryTypeLiteral extracts the string value from a ValueSpec value already
// known to be typed EntryType: either a bare string literal (the typed-const
// shape) or an EntryType(...)/journal.EntryType(...) conversion of one
// (redundant but legal). Anything else is reported rather than skipped.
func entryTypeLiteral(v ast.Expr, inJournal bool) (string, error) {
	switch e := v.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", fmt.Errorf("value is not a string literal")
		}
		return strconv.Unquote(e.Value)
	case *ast.CallExpr:
		if !isEntryTypeFunc(e.Fun, inJournal) || len(e.Args) != 1 {
			return "", fmt.Errorf("value is neither a string literal nor an EntryType(...) conversion of one")
		}
		lit, ok := e.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", fmt.Errorf("EntryType(...) conversion argument is not a string literal")
		}
		return strconv.Unquote(lit.Value)
	default:
		return "", fmt.Errorf("value is neither a string literal nor an EntryType(...) conversion of one")
	}
}

func nameList(names []*ast.Ident) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n.Name
	}
	return strings.Join(parts, ", ")
}

// RepoRoot walks up from the current working directory looking for go.mod,
// so ScanTree can be called the same way whether the process's cwd is the
// repo root (a direct `go run`) or a package directory (`go generate`,
// `go test`).
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("journalgen: no go.mod found above %s", dir)
		}
		dir = parent
	}
}
