package apidocs

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// node is one row of a rendered schema tree.
type node struct {
	Name        string // property name, "" at the root
	Type        string // display type: "object", "array of string", "string <uuid>", …
	Ref         string // component schema this node came from, if any
	Required    bool
	Nullable    bool
	Description string
	Enum        []string
	Note        string // why this branch stops, when it stops
	Children    []node
}

const (
	// maxTreeDepth bounds nesting. The deepest live schema in the generated
	// spec (the backup inspect response) is well inside this; the bound is
	// there so a future self-referential schema cannot turn a page render
	// into an infinite loop.
	maxTreeDepth = 12
	// maxTreeNodes bounds total work per page. A reader who hits it is told
	// so and pointed at the JSON, rather than served a silently short tree.
	maxTreeNodes = 4000
)

type treeBuilder struct {
	ix     *index
	budget int
}

func (ix *index) tree(s *schema, name string, required bool) node {
	b := &treeBuilder{ix: ix, budget: maxTreeNodes}
	return b.build(s, name, required, nil, 0)
}

func (b *treeBuilder) build(s *schema, name string, required bool, stack []string, depth int) node {
	n := node{Name: name, Required: required}
	if s == nil {
		n.Type = "any"
		return n
	}
	b.budget--
	if b.budget <= 0 {
		n.Type = typeLabel(s)
		n.Note = "tree truncated here — the full document is at /openapi.json"
		return n
	}
	if depth > maxTreeDepth {
		n.Type = typeLabel(s)
		n.Note = "nested deeper than this page renders — see /openapi.json"
		return n
	}

	// A $ref: name the target, link to it, and inline it once per branch.
	if s.Ref != "" {
		target := refName(s.Ref)
		if target == "" {
			n.Type = "unresolvable reference"
			n.Note = "the spec points at " + s.Ref + ", which this renderer cannot resolve"
			return n
		}
		n.Ref = target
		if contains(stack, target) {
			n.Type = target
			n.Note = "recursive reference — already expanded above"
			return n
		}
		resolved, ok := b.ix.schemaBy[target]
		if !ok || resolved.Schema == nil {
			n.Type = target
			n.Note = "dangling reference — no such schema in components"
			return n
		}
		inner := b.build(resolved.Schema, name, required, append(stack, target), depth)
		inner.Ref = target
		inner.Name = name
		inner.Required = required
		if inner.Description == "" {
			inner.Description = s.Description
		}
		return inner
	}

	n.Type = typeLabel(s)
	n.Nullable = s.Nullable
	n.Description = s.Description
	for _, e := range s.Enum {
		n.Enum = append(n.Enum, toString(e))
	}

	switch {
	case len(s.OneOf) > 0:
		n.Type = "one of"
		for i, sub := range s.OneOf {
			n.Children = append(n.Children, b.build(sub, fmt.Sprintf("option %d", i+1), false, stack, depth+1))
		}
	case len(s.AnyOf) > 0:
		n.Type = "any of"
		for i, sub := range s.AnyOf {
			n.Children = append(n.Children, b.build(sub, fmt.Sprintf("option %d", i+1), false, stack, depth+1))
		}
	case len(s.AllOf) > 0:
		n.Type = "all of"
		for i, sub := range s.AllOf {
			n.Children = append(n.Children, b.build(sub, fmt.Sprintf("part %d", i+1), false, stack, depth+1))
		}
	case s.Items != nil:
		n.Children = append(n.Children, b.build(s.Items, "items", false, stack, depth+1))
	}

	if len(s.Properties) > 0 {
		req := map[string]bool{}
		for _, r := range s.Required {
			req[r] = true
		}
		names := make([]string, 0, len(s.Properties))
		for p := range s.Properties {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			n.Children = append(n.Children, b.build(s.Properties[p], p, req[p], stack, depth+1))
		}
	}

	if len(s.AdditionalProperties) > 0 {
		var allowed bool
		if err := json.Unmarshal(s.AdditionalProperties, &allowed); err == nil {
			if !allowed {
				n.Note = strings.TrimSpace(n.Note + " no properties beyond those listed are accepted")
			}
		} else {
			var sub schema
			if err := json.Unmarshal(s.AdditionalProperties, &sub); err == nil {
				n.Children = append(n.Children, b.build(&sub, "«any other property»", false, stack, depth+1))
			}
		}
	}

	if s.Type == "object" && len(s.Properties) == 0 && len(s.AdditionalProperties) == 0 &&
		len(s.OneOf)+len(s.AnyOf)+len(s.AllOf) == 0 {
		// The generator's fallback body. Saying so is the point: a reader
		// who takes an unconstrained `object` for a documented shape has
		// been misled by the rendering, not by the spec.
		n.Note = "no properties described — the generator could not derive this body from the handler"
	}

	return n
}

func typeLabel(s *schema) string {
	t := s.Type
	if t == "" {
		switch {
		case len(s.Properties) > 0:
			t = "object"
		case s.Items != nil:
			t = "array"
		default:
			t = "any"
		}
	}
	if s.Format != "" {
		t += " <" + s.Format + ">"
	}
	return t
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
