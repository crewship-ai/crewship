package apidocs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaTreePreservesAlternativesAndAdditionalProperties(t *testing.T) {
	ix := &index{schemaBy: map[string]*schemaEntry{}}
	for _, tc := range []struct {
		name     string
		schema   *schema
		label    string
		children int
	}{
		{"one of", &schema{OneOf: []*schema{{Type: "string"}, {Type: "number"}}}, "one of", 2},
		{"any of", &schema{AnyOf: []*schema{{Type: "string"}, {Type: "null"}}}, "any of", 2},
		{"all of", &schema{AllOf: []*schema{{Type: "object"}, {Type: "object"}}}, "all of", 2},
		{"inferred array", &schema{Items: &schema{Type: "string"}}, "array", 1},
		{"inferred object", &schema{Properties: map[string]*schema{"value": {Type: "number"}}}, "object", 1},
		{"unspecified", &schema{}, "any", 0},
		{"absent", nil, "any", 0},
		{"typed additional properties", &schema{Type: "object", AdditionalProperties: json.RawMessage(`{"type":"integer"}`)}, "object", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := ix.tree(tc.schema, "body", true)
			if n.Name != "body" || !n.Required || n.Type != tc.label || len(n.Children) != tc.children {
				t.Fatalf("schema semantics lost: %+v", n)
			}
		})
	}
	closed := ix.tree(&schema{Type: "object", AdditionalProperties: json.RawMessage(`false`)}, "", false)
	if !strings.Contains(closed.Note, "no properties beyond") {
		t.Fatalf("closed object advertised as open: %+v", closed)
	}
	n := ix.tree(&schema{Type: "string", Nullable: true, Enum: []any{"ready", 17, true}, Description: "State"}, "status", true)
	if !n.Nullable || !n.Required || n.Description != "State" || strings.Join(n.Enum, ",") != "ready,17,true" {
		t.Fatalf("enum or nullability lost: %+v", n)
	}
}

func TestSchemaTreeExplainsRecursionMissingReferencesAndBounds(t *testing.T) {
	ix := &index{schemaBy: map[string]*schemaEntry{
		"Node":  {Name: "Node", Schema: &schema{Type: "object", Properties: map[string]*schema{"next": {Ref: schemaRefPrefix + "Node"}}}},
		"Empty": {Name: "Empty", Schema: &schema{Type: "string"}},
	}}
	recursive := ix.tree(&schema{Ref: schemaRefPrefix + "Node"}, "root", true)
	if len(recursive.Children) != 1 || !strings.Contains(recursive.Children[0].Note, "recursive reference") || recursive.Ref != "Node" {
		t.Fatalf("recursive reference was not bounded and explained: %+v", recursive)
	}
	for _, ref := range []string{"https://foreign.invalid/schema", schemaRefPrefix + "Missing"} {
		n := ix.tree(&schema{Ref: ref}, "root", false)
		if n.Note == "" || len(n.Children) != 0 {
			t.Fatalf("unresolvable reference silently expanded: %+v", n)
		}
	}
	n := ix.tree(&schema{Ref: schemaRefPrefix + "Empty", Description: "Reference description"}, "value", true)
	if n.Description != "Reference description" || n.Ref != "Empty" || !n.Required {
		t.Fatalf("reference annotations lost: %+v", n)
	}
	deep := &schema{Type: "string"}
	for range maxTreeDepth + 2 {
		deep = &schema{Type: "array", Items: deep}
	}
	n = ix.tree(deep, "root", false)
	for len(n.Children) > 0 {
		n = n.Children[0]
	}
	if !strings.Contains(n.Note, "nested deeper") || !strings.Contains(n.Note, "/openapi.json") {
		t.Fatalf("depth limit silently omitted schema: %+v", n)
	}
	wide := &schema{Type: "object", Properties: map[string]*schema{}}
	for i := range maxTreeNodes + 1 {
		wide.Properties[string(rune(0x1000+i))] = &schema{Type: "string"}
	}
	n = ix.tree(wide, "root", false)
	truncated := false
	for _, child := range n.Children {
		if strings.Contains(child.Note, "tree truncated") {
			truncated = true
		}
	}
	if !truncated {
		t.Fatal("large schema gave no truncation notice")
	}
}

func TestIndexHandlesMissingOperationIDsAndDanglingSchemaReferences(t *testing.T) {
	ix, err := newIndex([]byte(`{"paths":{"/items/{id}":{"parameters":{},"get":{"parameters":[null,{"schema":{"$ref":"#/components/schemas/Missing"}}],"responses":{"200":null}},"post":null}},"components":{"schemas":{"Unused":{"type":"string"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.ops) != 1 || ix.ops[0].ID != "get_items_id" || ix.ops[0].Tags[0] != "untagged" || ix.UnreachableSchemas != 1 {
		t.Fatalf("index lost valid unnamed operation: %+v", ix)
	}
	if refs := ix.directRefs(nil); len(refs) != 0 {
		t.Fatalf("nil operation invented references: %v", refs)
	}
}
