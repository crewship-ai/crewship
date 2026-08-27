package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Unit tests for the path extractor in cli_route_contract_test.go.
//
// The contract guard itself only reports paths the ROUTER does not register,
// so a bug that makes the extractor render fewer paths is invisible there:
// the guard stays green and simply checks less. That is how `+=` went
// unnoticed — every path built by accumulation was silently dropped, and the
// only way to see it was to mutate a command to call a route that does not
// exist and watch the guard pass anyway.
//
// These tests close that blind spot from the other side: they assert what the
// extractor renders for a given call site, so a rendering that loses a path
// fails here rather than quietly shrinking the guard's coverage.

// renderedSites parses src as a Go file, runs the real resolver over it, and
// returns the "METHOD /path" set for the first function declaration's body.
func renderedSites(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	resolver := newPathResolver([]*ast.File{f})

	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "target" {
			continue
		}
		for _, s := range callSitesInBody(fset, resolver, fn.Body) {
			out = append(out, s.Method+" "+s.Path)
		}
	}
	sort.Strings(out)
	return dedupMethods(out)
}

func TestExtractorRendersAccumulatedPaths(t *testing.T) {
	const header = "package main\n\n"

	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			// The regression this file exists for. `path += "/" + slot`
			// used to be recorded as the bare `"/" + slot`, which renders
			// `/{}` and is dropped by the `/api/` prefix filter — so the
			// only candidate left was the base and `…/aux/{}` was never
			// checked against the router at all.
			name: "conditional segment append yields base and suffix",
			body: `path := "/api/v1/admin/keeper/aux"
	if slot != "" {
		path += "/" + slot
	}
	deleteJSON(client, path)`,
			want: []string{
				"DELETE /api/v1/admin/keeper/aux",
				"DELETE /api/v1/admin/keeper/aux/{}",
			},
		},
		{
			// A literal appended segment must land in the rendered path, or
			// a command could call a misspelt route and still pass.
			name: "literal segment append is part of the path",
			body: `path := "/api/v1/agents"
	path += "/summary"
	getJSON(client, path, &out)`,
			want: []string{
				"GET /api/v1/agents",
				"GET /api/v1/agents/summary",
			},
		},
		{
			name: "two appends accumulate in order",
			body: `path := "/api/v1/crews"
	path += "/" + id
	path += "/members"
	getJSON(client, path, &out)`,
			want: []string{
				"GET /api/v1/crews",
				"GET /api/v1/crews/{}",
				"GET /api/v1/crews/{}/members",
			},
		},
		{
			// The overwhelmingly common `+=` in the CLI. normalisePath drops
			// the query, so the appended half must not leak into the path.
			name: "query append normalises back to the base path",
			body: `path := "/api/v1/runs"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	getJSON(client, path, &out)`,
			want: []string{"GET /api/v1/runs"},
		},
		{
			// Pre-existing behaviour that must survive: flow-insensitive
			// reassignment yields every arm.
			name: "plain reassignment still yields both arms",
			body: `path := "/api/v1/inbox"
	if archived {
		path = "/api/v1/inbox/archived"
	}
	getJSON(client, path, &out)`,
			want: []string{
				"GET /api/v1/inbox",
				"GET /api/v1/inbox/archived",
			},
		},
		{
			// An accumulation onto a name whose starting value we cannot see
			// must stay unresolved rather than render as if the append were
			// the whole path — that would be an invented call site.
			name: "append onto an opaque base stays unresolved",
			body: `path := buildSomething()
	path += "/api/v1/not-really-the-path"
	getJSON(client, path, &out)`,
			want: nil,
		},
		{
			// Guards the token switch in bodyScope: before it, EVERY
			// single-value assignment was recorded as if it set the whole
			// value, so `x -= e` claimed x holds e. Parsed, never typechecked,
			// so the snippet only has to be syntactically valid.
			name: "a non-additive compound assignment is not a value",
			body: `path := "/api/v1/agents"
	path -= "/api/v1/invented"
	getJSON(client, path, &out)`,
			want: []string{"GET /api/v1/agents"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderedSites(t, header+"func target() {\n\t"+tc.body+"\n}\n")
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("rendered call sites =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}
