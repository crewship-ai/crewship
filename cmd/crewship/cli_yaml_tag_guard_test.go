package main

// Package-wide form of the rule in cli_yaml_key_parity_test.go: every struct
// field in cmd/crewship that carries a `json:` tag carries a `yaml:` tag that
// names the same key with the same omitempty. The reflection test there walks
// the types it is told about, including ones from other packages; this one
// reads the source of every non-test file in the package, so a struct that
// nobody remembered to list is covered the moment it is written.
//
// Why source and not reflection: the types that reach a formatter are not a
// closed set. A request body decoded from a server response is one refactor
// away from being handed to newFormatter().Auto, and a local
// `type row struct` inside a RunE is invisible to any list. Reading every
// StructType in the package is the only way to make the rule hold by
// construction rather than by memory. The cost is that request bodies which
// never reach yaml carry a tag they do not need; that is a few bytes per
// field, and it is what makes the rule simple enough to state in one line.
//
// The rule is strict on purpose — a `yaml:` tag is required even where the
// lowercased Go name happens to equal the json key — because "mirror the json
// tag" is a rule a reviewer can check without knowing yaml.v3's fallback,
// and a later rename of the json key cannot silently reopen the gap.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestEveryJSONTaggedFieldMirrorsItsYAMLTag(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	var findings []string
	checked := 0
	parsed := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		parsed++
		n, found := yamlTagFindings(fset, f)
		checked += n
		findings = append(findings, found...)
	}

	if parsed == 0 {
		t.Fatal("parsed no source files — is the test running from cmd/crewship?")
	}
	// Anti-vacuity: the package holds thousands of json-tagged fields. If the
	// walker ever reports a handful, it has gone blind, not the package clean.
	if checked < 1000 {
		t.Fatalf("inspected only %d json-tagged field(s) across %d files — the walker has gone blind", checked, parsed)
	}
	if len(findings) > 0 {
		t.Errorf("%d struct field(s) whose yaml tag does not mirror the json tag, so -f yaml and -f json can disagree on the key (#1211, #2119).\n"+
			"gopkg.in/yaml.v3 does not read json tags: add a `yaml:` tag mirroring the json name and omitempty.\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}

// yamlTagFindings inspects every struct type in f and returns the number of
// json-tagged fields it saw plus one line per field that breaks the rule.
func yamlTagFindings(fset *token.FileSet, f *ast.File) (int, []string) {
	labels := structLabels(f)
	var findings []string
	checked := 0
	report := func(fld *ast.Field, label, msg string) {
		pos := fset.Position(fld.Pos())
		name := "(embedded)"
		if len(fld.Names) > 0 {
			name = fld.Names[0].Name
		}
		findings = append(findings, fmt.Sprintf("  %s:%d: %s.%s: %s", filepath.Base(pos.Filename), pos.Line, label, name, msg))
	}

	ast.Inspect(f, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		label := labels[st]
		if label == "" {
			label = "struct"
		}
		hasJSONSibling := false
		for _, fld := range st.Fields.List {
			if fld.Tag != nil && strings.Contains(fld.Tag.Value, `json:"`) {
				hasJSONSibling = true
				break
			}
		}
		for _, fld := range st.Fields.List {
			embedded := len(fld.Names) == 0
			var tag reflect.StructTag
			if fld.Tag != nil {
				raw, err := strconv.Unquote(fld.Tag.Value)
				if err != nil {
					report(fld, label, "unparseable struct tag "+fld.Tag.Value)
					continue
				}
				tag = reflect.StructTag(raw)
			}
			jsonTag, hasJSON := tag.Lookup("json")
			yamlTag, hasYAML := tag.Lookup("yaml")
			if !hasJSON {
				// An untagged embedded struct next to json-tagged siblings is
				// flattened by encoding/json and nested under its lowercased
				// type name by yaml.v3.
				if embedded && hasJSONSibling && !(hasYAML && hasOpt(yamlTag, "inline")) {
					report(fld, label, "embedded field is flattened by -f json and nested by -f yaml; "+
						"add `json:\",inline\" yaml:\",inline\"`")
				}
				continue
			}
			checked++
			jsonName, jsonOpts, _ := strings.Cut(jsonTag, ",")
			yamlName, yamlOpts, _ := strings.Cut(yamlTag, ",")

			switch {
			case jsonTag == "-":
				// encoding/json drops the field; yaml.v3 would still emit it.
				// On a credential-bearing struct that is a leak, not a nit.
				if yamlTag != "-" {
					report(fld, label, "`json:\"-\"` without `yaml:\"-\"` — omitted from -f json, still emitted by -f yaml")
				}
			case embedded && jsonName == "":
				if !hasYAML || !hasOpt(yamlOpts, "inline") {
					report(fld, label, "embedded field is flattened by -f json and nested by -f yaml; add `yaml:\",inline\"`")
				}
			case !hasYAML:
				want := jsonName
				if want == "" {
					want = fld.Names[0].Name
				}
				got := strings.ToLower(fld.Names[0].Name)
				if got != want || hasOpt(jsonOpts, "omitempty") {
					report(fld, label, fmt.Sprintf("json:%q has no yaml tag (-f yaml would use %q) — add `yaml:%q`",
						jsonTag, got, mirrorYAMLTag(jsonTag)))
				} else {
					report(fld, label, fmt.Sprintf("json:%q has no yaml tag — add `yaml:%q` so the mirror is explicit",
						jsonTag, mirrorYAMLTag(jsonTag)))
				}
			default:
				want := jsonName
				if want == "" {
					want = fld.Names[0].Name
				}
				got := yamlName
				if got == "" {
					got = strings.ToLower(fld.Names[0].Name)
				}
				if got != want {
					report(fld, label, fmt.Sprintf("-f json key %q but -f yaml key %q", want, got))
				}
				if hasOpt(jsonOpts, "omitempty") != hasOpt(yamlOpts, "omitempty") {
					report(fld, label, fmt.Sprintf("omitempty differs (json:%q yaml:%q) — the two documents differ on an empty value",
						jsonTag, yamlTag))
				}
			}
		}
		return true
	})
	return checked, findings
}

// structLabels names every struct type in f for the failure message: a
// declared type by its name, a struct-typed field by Outer.Field, anything
// else (a literal in a function body) falls back to "struct".
func structLabels(f *ast.File) map[*ast.StructType]string {
	labels := map[*ast.StructType]string{}
	var walk func(st *ast.StructType, label string)
	walk = func(st *ast.StructType, label string) {
		labels[st] = label
		if st.Fields == nil {
			return
		}
		for _, fld := range st.Fields.List {
			inner, ok := unwrapStruct(fld.Type)
			if !ok {
				continue
			}
			name := "(embedded)"
			if len(fld.Names) > 0 {
				name = fld.Names[0].Name
			}
			walk(inner, label+"."+name)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		if st, ok := unwrapStruct(ts.Type); ok {
			walk(st, ts.Name.Name)
		}
		return true
	})
	return labels
}

// unwrapStruct sees through pointers, slices, arrays and map values so that
// `Rows []struct{...}` and `Meta map[string]*struct{...}` are labelled.
func unwrapStruct(e ast.Expr) (*ast.StructType, bool) {
	for {
		switch t := e.(type) {
		case *ast.StructType:
			return t, true
		case *ast.StarExpr:
			e = t.X
		case *ast.ArrayType:
			e = t.Elt
		case *ast.MapType:
			e = t.Value
		case *ast.ParenExpr:
			e = t.X
		default:
			return nil, false
		}
	}
}

// mirrorYAMLTag derives the yaml tag that names the same key as jsonTag:
// the name and omitempty carry over, json-only options such as `string`
// do not (yaml.v3 rejects flags it does not know).
func mirrorYAMLTag(jsonTag string) string {
	if jsonTag == "-" {
		return "-"
	}
	name, opts, _ := strings.Cut(jsonTag, ",")
	if hasOpt(opts, "omitempty") {
		return name + ",omitempty"
	}
	return name
}

func hasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// Anti-vacuity for the source walker: feed it a file with every kind of
// mismatch and assert each one is named, then a clean file and assert silence.
func TestYAMLTagGuardCatchesEachKindOfMismatch(t *testing.T) {
	t.Parallel()

	const bad = `package p

type Inner struct {
	Deep string ` + "`json:\"deep_field\"`" + `
}

type Bad struct {
	Inner
	ConfigFile string  ` + "`json:\"config_file\"`" + `
	Timestamp  string  ` + "`json:\"ts\"`" + `
	Secret     string  ` + "`json:\"-\"`" + `
	Optional   string  ` + "`json:\"optional,omitempty\" yaml:\"optional\"`" + `
	Renamed    string  ` + "`json:\"renamed\" yaml:\"other\"`" + `
	Fine       string  ` + "`json:\"fine\" yaml:\"fine\"`" + `
	Rows       []struct {
		LineStart int ` + "`json:\"line_start\"`" + `
	} ` + "`json:\"rows\" yaml:\"rows\"`" + `
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "bad.go", bad, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	n, findings := yamlTagFindings(fset, f)
	if n != 9 {
		t.Errorf("inspected %d json-tagged fields, want 9", n)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{
		"bad.go:8: Bad.(embedded): embedded field is flattened",
		"bad.go:9: Bad.ConfigFile: json:\"config_file\" has no yaml tag",
		"bad.go:10: Bad.Timestamp: json:\"ts\" has no yaml tag",
		"bad.go:11: Bad.Secret: `json:\"-\"` without `yaml:\"-\"`",
		"bad.go:12: Bad.Optional: omitempty differs",
		"bad.go:13: Bad.Renamed: -f json key \"renamed\" but -f yaml key \"other\"",
		"bad.go:16: Bad.Rows.LineStart: json:\"line_start\" has no yaml tag",
		"bad.go:4: Inner.Deep: json:\"deep_field\" has no yaml tag",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing finding %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "Bad.Fine") {
		t.Errorf("clean field reported:\n%s", joined)
	}
	if len(findings) != 8 {
		t.Errorf("got %d findings, want 8:\n%s", len(findings), joined)
	}

	const good = `package p

type Good struct {
	Inner      ` + "`json:\",inline\" yaml:\",inline\"`" + `
	ConfigFile string ` + "`json:\"config_file\" yaml:\"config_file\"`" + `
	Secret     string ` + "`json:\"-\" yaml:\"-\"`" + `
	Optional   string ` + "`json:\"optional,omitempty\" yaml:\"optional,omitempty\"`" + `
	Count      int    ` + "`json:\"count,string\" yaml:\"count\"`" + `
	untagged   string
}
`
	f, err = parser.ParseFile(fset, "good.go", good, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if n, findings := yamlTagFindings(fset, f); len(findings) != 0 || n != 5 {
		t.Errorf("clean file: inspected %d (want 5), findings:\n%s", n, strings.Join(findings, "\n"))
	}
}
