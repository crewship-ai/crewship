package main

import (
	"sort"
	"strings"
	"testing"
)

// Every component schema must be reachable from some operation. A schema no
// operation names is dead weight in a generated client, and — worse — it is a
// plausible name a caller reaches for (`CrewCreateRequest`) that the API never
// accepts. #1849 found 62 of them; this gate makes reintroducing one a test
// failure that names it, instead of something noticed a release later.
//
// Reachability is the transitive closure of `$ref` starting from every
// operation's parameters, requestBody and responses, walking through
// allOf/oneOf/anyOf, items, properties, additionalProperties and the
// nested schema of parameters and media types alike.
func TestOpenAPIComponentSchemas_AreAllReachable(t *testing.T) {
	doc := loadSpec(t)
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if len(schemas) == 0 {
		t.Fatal("spec has no component schemas")
	}

	reachable := reachableSchemas(doc)
	var dead []string
	for name := range schemas {
		if !reachable[name] {
			dead = append(dead, name)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("%d of %d component schemas are unreachable from every operation (%d reachable).\n"+
			"Either wire the route that should reference each one, or drop it from its cmd/gen-openapi/schemas_*.go catalog:\n  %s",
			len(dead), len(schemas), len(reachable), strings.Join(dead, "\n  "))
	}
}

// The mirror image: a $ref to a component that does not exist. The catalogs
// override one another by route, so a schema can be deleted from the file that
// defined it while a stale ref to it survives in another; that ref is a
// generated-client compile error, and nothing before this gate reported it.
func TestOpenAPIRefs_AllResolve(t *testing.T) {
	doc := loadSpec(t)
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	dangling := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(r, "#/components/schemas/")
				if name == r || schemas[name] == nil {
					dangling[r] = true
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	if len(dangling) > 0 {
		var names []string
		for r := range dangling {
			names = append(names, r)
		}
		sort.Strings(names)
		t.Errorf("%d $ref targets do not exist in components.schemas:\n  %s", len(names), strings.Join(names, "\n  "))
	}
}

// reachableSchemas returns the set of component names in the $ref closure of
// the document's operations.
func reachableSchemas(doc map[string]any) map[string]bool {
	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	seen := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				const prefix = "#/components/schemas/"
				if name := strings.TrimPrefix(r, prefix); name != r && !seen[name] {
					seen[name] = true
					walk(schemas[name])
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		case []map[string]any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	paths, _ := doc["paths"].(map[string]any)
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, op := range methods {
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			walk(operation["parameters"])
			walk(operation["requestBody"])
			walk(operation["responses"])
		}
	}
	return seen
}
