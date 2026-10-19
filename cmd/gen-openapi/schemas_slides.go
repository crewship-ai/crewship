package main

// The public reader admits only native schema 1. Dormant image storage does not
// become a supported read format or an asset permission through this contract.
func slidesSchemaComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(min int) map[string]any { return map[string]any{"type": "integer", "minimum": min} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	theme := obj(map[string]any{"background": str(), "color": str(), "font_family": str(), "font_size": integer(1)}, "background", "color", "font_family", "font_size")
	text := obj(map[string]any{"value": str(), "font_family": str(), "font_size": integer(1), "color": str(), "align": map[string]any{"type": "string", "enum": []string{"left", "center", "right"}}}, "value", "font_family", "font_size", "color", "align")
	shape := obj(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"rectangle", "ellipse"}}, "fill": str(), "stroke": str(), "stroke_width": integer(0)}, "kind", "fill", "stroke", "stroke_width")
	table := obj(map[string]any{"cells": arr(arr(str())), "font_family": str(), "font_size": integer(1), "color": str(), "fill": str(), "border": str()}, "cells", "font_family", "font_size", "color", "fill", "border")
	element := obj(map[string]any{"id": str(), "type": map[string]any{"type": "string", "enum": []string{"text", "shape", "table"}}, "x": integer(0), "y": integer(0), "width": integer(1), "height": integer(1), "text": text, "shape": shape, "table": table}, "id", "type", "x", "y", "width", "height")
	alternatives := []any{}
	for _, kind := range []string{"text", "shape", "table"} {
		excluded := []any{}
		for _, other := range []string{"text", "shape", "table"} {
			if kind != other {
				excluded = append(excluded, map[string]any{"required": []string{other}})
			}
		}
		alternatives = append(alternatives, map[string]any{"properties": map[string]any{"type": map[string]any{"enum": []string{kind}}}, "required": []string{kind}, "not": map[string]any{"anyOf": excluded}})
	}
	element["oneOf"] = alternatives
	slide := obj(map[string]any{"id": str(), "notes": str(), "elements": arr(element)}, "id", "notes", "elements")
	version := func() map[string]any { return map[string]any{"type": "integer", "enum": []int{1}} }
	deck := obj(map[string]any{"schema_version": version(), "deck_id": str(), "width": integer(64), "height": integer(64), "theme": theme, "slide_order": arr(str()), "slides": arr(slide)}, "schema_version", "deck_id", "width", "height", "theme", "slide_order", "slides")
	deck["description"] = "Native schema1: bounded geometry, text, element count and table-cell budgets are validated by the Go store. Unknown fields, images and future schemas are refused."
	return map[string]any{"SlidesRead": obj(map[string]any{"page_id": str(), "schema_version": version(), "revision": integer(1), "order_version": integer(1), "content_hash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "deck": deck}, "page_id", "schema_version", "revision", "order_version", "content_hash", "deck")}
}
func slidesSchemaCatalog() map[string]DomainSchema {
	return map[string]DomainSchema{"GET /api/v1/pages/{slug}/slides": {Response: ref("SlidesRead"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "500", "503"}}}
}
